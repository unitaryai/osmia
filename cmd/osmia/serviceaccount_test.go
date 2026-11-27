package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCheckAgentServiceAccountExists(t *testing.T) {
	unreadable := fake.NewClientset()
	unreadable.PrependReactor("get", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, "osmia-agent", nil)
	})
	tests := []struct {
		name    string
		client  *fake.Clientset
		wantErr bool
	}{
		{
			name: "account exists",
			client: fake.NewClientset(&corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: "osmia-agent", Namespace: testNamespace},
			}),
		},
		{name: "account missing", client: fake.NewClientset(), wantErr: true},
		{name: "account unreadable", client: unreadable, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkAgentServiceAccountExists(context.Background(), tt.client, testNamespace, "osmia-agent")

			assert.Equal(t, tt.wantErr, err != nil, "error: %v", err)
		})
	}
}

func TestCheckNotControllerServiceAccount(t *testing.T) {
	tests := []struct {
		name                string
		agentNamespace      string
		controllerAccount   string
		controllerNamespace string
		wantErr             bool
	}{
		{name: "controller's account in its namespace", agentNamespace: "osmia", controllerAccount: "osmia", controllerNamespace: "osmia", wantErr: true},
		{name: "same name in another namespace", agentNamespace: "agents", controllerAccount: "osmia", controllerNamespace: "osmia"},
		{name: "another account", agentNamespace: "osmia", controllerAccount: "osmia-controller", controllerNamespace: "osmia"},
		{name: "controller's account not known", agentNamespace: "osmia"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkNotControllerServiceAccount("osmia", tt.agentNamespace, tt.controllerAccount, tt.controllerNamespace)

			assert.Equal(t, tt.wantErr, err != nil, "error: %v", err)
		})
	}
}
