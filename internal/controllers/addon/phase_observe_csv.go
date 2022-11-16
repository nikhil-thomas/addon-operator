package addon

import (
	"context"
	"fmt"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	addonsv1alpha1 "github.com/openshift/addon-operator/apis/addons/v1alpha1"
)

func (r *olmReconciler) observeCurrentCSV(
	ctx context.Context,
	addon *addonsv1alpha1.Addon,
	csvKey client.ObjectKey,
) (requeueResult, error) {
	operator := &operatorsv1.Operator{}
	operatorKey := client.ObjectKey{
		Namespace: "",
		Name:      "reference-addon.reference-addon-ns",
	}
	if err := r.uncachedClient.Get(ctx, operatorKey, operator); err != nil {
		return resultNil, fmt.Errorf("getting installed CSV: %w", err)
	}

	var message string
	phase := csvSucceeded(csvKey, operator)
	switch phase {
	case operatorsv1alpha1.CSVPhaseSucceeded:
		// do nothing here
	case operatorsv1alpha1.CSVPhaseFailed:
		message = "failed"
	default:
		message = "unkown/pending"
	}

	if message != "" {
		reportUnreadyCSV(addon, message)
		return resultRetry, nil
	}

	return resultNil, nil
}

func csvSucceeded(csv client.ObjectKey, operator *operatorsv1.Operator) operatorsv1alpha1.ClusterServiceVersionPhase {
	components := operator.Status.Components
	if components == nil {
		return ""
	}
	for _, component := range components.Refs {
		if component.Kind != "ClusterServiceVersion" {
			continue
		}
		if component.Name != csv.Name || component.Namespace != csv.Namespace {
			continue
		}
		compConditions := component.Conditions
		for _, c := range compConditions {
			if c.Type == "Succeeded" {
				if c.Status == "True" {
					fmt.Println("::::::::::; TADA succeeded")
					return operatorsv1alpha1.CSVPhaseSucceeded
				} else {
					fmt.Println("::::::::::; TADA FAILED")

					return operatorsv1alpha1.CSVPhaseFailed
				}
			}
		}
	}
	return ""
}
