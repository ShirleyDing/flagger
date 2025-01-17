/*
Copyright 2020 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package canary

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flaggerv1 "github.com/fluxcd/flagger/pkg/apis/flagger/v1beta1"
)

// IsPrimaryReady checks the primary daemonset status and returns an error if
// the daemonset is in the middle of a rolling update
func (c *DaemonSetController) IsPrimaryReady(cd *flaggerv1.Canary) error {
	primaryName := fmt.Sprintf("%s-primary", cd.Spec.TargetRef.Name)
	primary, err := c.kubeClient.AppsV1().DaemonSets(cd.Namespace).Get(context.TODO(), primaryName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("daemonset %s.%s get query error: %w", primaryName, cd.Namespace, err)
	}

	_, err = c.isDaemonSetReady(cd, primary, cd.GetAnalysisPrimaryReadyThreshold())
	if err != nil {
		return fmt.Errorf("primary daemonset %s.%s not ready: %w", primaryName, cd.Namespace, err)
	}
	return nil
}

// IsCanaryReady checks the primary daemonset and returns an error if
// the daemonset is in the middle of a rolling update
func (c *DaemonSetController) IsCanaryReady(cd *flaggerv1.Canary) (bool, error) {
	targetName := cd.Spec.TargetRef.Name
	canary, err := c.kubeClient.AppsV1().DaemonSets(cd.Namespace).Get(context.TODO(), targetName, metav1.GetOptions{})
	if err != nil {
		return true, fmt.Errorf("daemonset %s.%s get query error: %w", targetName, cd.Namespace, err)
	}

	retryable, err := c.isDaemonSetReady(cd, canary, 100)
	if err != nil {
		return retryable, fmt.Errorf("canary damonset %s.%s not ready with retryable %v: %w",
			targetName, cd.Namespace, retryable, err)
	}
	return true, nil
}

// isDaemonSetReady determines if a daemonset is ready by checking the number of old version daemons
// reference: https://github.com/kubernetes/kubernetes/blob/5232ad4a00ec93942d0b2c6359ee6cd1201b46bc/pkg/kubectl/rollout_status.go#L110
func (c *DaemonSetController) isDaemonSetReady(cd *flaggerv1.Canary, daemonSet *appsv1.DaemonSet, readyThreshold int) (bool, error) {
	if daemonSet.Generation <= daemonSet.Status.ObservedGeneration {
		readyThresholdRatio := float32(readyThreshold) / float32(100)

		// calculate conditions
		newCond := daemonSet.Status.UpdatedNumberScheduled < daemonSet.Status.DesiredNumberScheduled
		readyThresholdDesiredReplicas := int32(float32(daemonSet.Status.DesiredNumberScheduled) * readyThresholdRatio)
		availableCond := daemonSet.Status.NumberAvailable < readyThresholdDesiredReplicas
		if !newCond && !availableCond {
			return true, nil
		}

		// check if deadline exceeded
		from := cd.Status.LastTransitionTime
		delta := time.Duration(cd.GetProgressDeadlineSeconds()) * time.Second
		if from.Add(delta).Before(time.Now()) {
			return false, fmt.Errorf("exceeded its progressDeadlineSeconds: %d", cd.GetProgressDeadlineSeconds())
		}

		// retryable
		if newCond {
			return true, fmt.Errorf("waiting for rollout to finish: %d out of %d new pods have been updated",
				daemonSet.Status.UpdatedNumberScheduled, daemonSet.Status.DesiredNumberScheduled)
		} else if availableCond {
			return true, fmt.Errorf("waiting for rollout to finish: %d of %d (readyThreshold %d%%) updated pods are available",
				daemonSet.Status.NumberAvailable, readyThresholdDesiredReplicas, readyThreshold)
		}
	}
	return true, fmt.Errorf("waiting for rollout to finish: observed daemonset generation less than desired generation")
}
