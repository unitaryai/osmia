package main

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// checkAgentServiceAccountExists returns an error unless the service account
// agent pods run as can be read in namespace, so that a mistyped name stops
// the controller at startup instead of leaving every agent job without a pod.
func checkAgentServiceAccountExists(ctx context.Context, client kubernetes.Interface, namespace, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := client.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("execution.service_account_name: service account %q does not exist in namespace %q", name, namespace)
	}
	if err != nil {
		return fmt.Errorf("execution.service_account_name: reading service account %q in namespace %q, which needs get on serviceaccounts: %w", name, namespace, err)
	}
	return nil
}

// checkNotControllerServiceAccount returns an error when agent pods would run
// as the controller's own service account, and so hold its rights to the
// cluster. An unknown controller account allows any name.
func checkNotControllerServiceAccount(name, namespace, controllerAccount, controllerNamespace string) error {
	if name == controllerAccount && namespace == controllerNamespace {
		return fmt.Errorf("execution.service_account_name: %q is the controller's own service account in namespace %q", name, namespace)
	}
	return nil
}
