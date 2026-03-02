/*
Copyright 2025 The Kubernetes Authors.

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

package bbr

import (
	"context"
	"testing"

	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/bbr/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/bbr/handlers"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/bbr/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/bbr/plugins/basemodelextractor"
	runserver "sigs.k8s.io/gateway-api-inference-extension/pkg/bbr/server"
	bbrtest "sigs.k8s.io/gateway-api-inference-extension/pkg/bbr/test"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/test/integration"
)

var logger = logutil.NewTestLogger().V(logutil.VERBOSE)

// BBRHarness encapsulates the environment for a single isolated BBR test run.
type BBRHarness struct {
	t      *testing.T
	Client extProcPb.ExternalProcessor_ProcessClient

	// Internal handles for cleanup
	server   *runserver.ExtProcServerRunner
	grpcConn *grpc.ClientConn
}

// NewBBRHarness boots up an isolated BBR server on a random port with the default
// BodyFieldToHeaderPlugin for model extraction and no response plugins.
func NewBBRHarness(t *testing.T, ctx context.Context, streaming bool) *BBRHarness {
	t.Helper()
	modelToHeaderPlugin, err := plugins.NewBodyFieldToHeaderPlugin(handlers.ModelField, handlers.ModelHeader)
	require.NoError(t, err, "failed to create body-field-to-header plugin")

	baseModelToHeaderPlugin, err := bbrtest.NewTestBaseModelPlugin()
	require.NoError(t, err, "failed to create base model plugin")

	return NewBBRHarnessWithPlugins(t, ctx, streaming, []framework.RequestProcessor{modelToHeaderPlugin, baseModelToHeaderPlugin}, nil)
}

// NewBBRHarnessWithPlugins boots up an isolated BBR server on a random port
// with the given request and response plugins.
func NewBBRHarnessWithPlugins(
	t *testing.T,
	ctx context.Context,
	streaming bool,
	requestPlugins []framework.RequestProcessor,
	responsePlugins []framework.ResponseProcessor,
) *BBRHarness {
	t.Helper()

	// 1. Allocate Free Port
	port, err := integration.GetFreePort()
	require.NoError(t, err, "failed to acquire free port for BBR server")

	// 2. Configure BBR Server with plugins
	runner := runserver.NewDefaultExtProcServerRunner(port, false)
	runner.SecureServing = false
	runner.Streaming = streaming
	if requestPlugins == nil {
		modelToHeaderPlugin, err := plugins.NewBodyFieldToHeaderPlugin(handlers.ModelField, handlers.ModelHeader)
		require.NoError(t, err, "failed to create body-field-to-header plugin")

		baseModelPlugin, err := bbrtest.NewTestBaseModelPlugin()
		require.NoError(t, err, "failed to create base model plugin")

		requestPlugins = []framework.RequestProcessor{modelToHeaderPlugin, baseModelPlugin}
	}
	runner.RequestPlugins = requestPlugins
	runner.ResponsePlugins = responsePlugins

	// Find the BaseModelToHeaderPlugin in the requestPlugins to configure it
	var baseModelToHeaderPlugin *basemodelextractor.BaseModelToHeaderPlugin
	for _, plugin := range requestPlugins {
		if p, ok := plugin.(*basemodelextractor.BaseModelToHeaderPlugin); ok {
			baseModelToHeaderPlugin = p
			break
		}
	}

	// Configure the BaseModelToHeaderPlugin with test data if it exists
	if baseModelToHeaderPlugin != nil {
		testConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-model-mappings",
				Namespace: "default",
				Labels: map[string]string{
					"inference.networking.k8s.io/bbr-managed": "true",
				},
			},
			Data: map[string]string{
				"baseModel": "llama",
				"adapters": `
- sql-lora-sheddable
- foo
- 1
`,
			},
		}

		reconciler := baseModelToHeaderPlugin.GetReconciler()

		fakeClient := fake.NewClientBuilder().
			WithObjects(testConfigMap).
			Build()

		reconciler.Reader = fakeClient

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: testConfigMap.Namespace,
				Name:      testConfigMap.Name,
			},
		}
		_, err = reconciler.Reconcile(ctx, req)
		require.NoError(t, err, "failed to configure base model plugin with test data via Reconcile")
	}

	// 3. Start Server in Background
	serverCtx, serverCancel := context.WithCancel(ctx)

	runnable := runner.AsRunnable(logger.WithName("bbr-server")).Start
	client, conn := integration.StartExtProcServer(
		t,
		serverCtx,
		runnable,
		port,
		logger,
	)

	h := &BBRHarness{
		t:        t,
		Client:   client,
		server:   runner,
		grpcConn: conn,
	}

	// 4. Register Cleanup
	t.Cleanup(func() {
		logger.Info("Tearing down BBR server", "port", port)
		serverCancel()
		if err := h.grpcConn.Close(); err != nil {
			t.Logf("Warning: failed to close grpc connection: %v", err)
		}
	})

	return h
}
