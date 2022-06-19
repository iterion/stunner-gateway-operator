package renderer

import (
	// "context"
	// "fmt"
	"github.com/stretchr/testify/assert"
	"testing"

	corev1 "k8s.io/api/core/v1"
	// metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// "k8s.io/apimachinery/pkg/types"
	// "sigs.k8s.io/controller-runtime/pkg/log/zap"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	// "github.com/l7mp/stunner-gateway-operator/internal/event"
	"github.com/l7mp/stunner-gateway-operator/internal/operator"

	stunnerv1alpha1 "github.com/l7mp/stunner-gateway-operator/api/v1alpha1"
	stunnerconfv1alpha1 "github.com/l7mp/stunner/pkg/apis/v1alpha1"
)

func TestRenderE2E(t *testing.T) {
	renderTester(t, []renderTestConfig{
		{
			name: "piecewise render",
			cls:  []gatewayv1alpha2.GatewayClass{testGwClass},
			cfs:  []stunnerv1alpha1.GatewayConfig{testGwConfig},
			gws:  []gatewayv1alpha2.Gateway{},
			rs:   []gatewayv1alpha2.UDPRoute{},
			svcs: []corev1.Service{},
			prep: func(c *renderTestConfig) {},
			tester: func(t *testing.T, r *Renderer) {
				gc, err := r.getGatewayClass()
				assert.NoError(t, err, "gw-class found")
				assert.Equal(t, gc.GetName(), "gatewayclass-ok")

				gwConf, err := r.getGatewayConfig4Class(gc)
				assert.NoError(t, err, "gw-conf found")
				assert.Equal(t, gwConf.GetName(), "gatewayconfig-ok", "gatewayconfig name")

				admin, err := r.renderAdmin(gwConf)
				assert.NoError(t, err, "admin rendered")
				assert.Equal(t, admin.LogLevel, "testloglevel", "log level")
				assert.Equal(t, admin.Name, operator.DefaultStunnerdInstanceName, "stunnerd name")

				auth, err := r.renderAuth(gwConf)
				assert.NoError(t, err, "auth rendered")
				assert.Equal(t, auth.Type, stunnerconfv1alpha1.AuthTypePlainText.String(), "auth type")
				assert.Equal(t, auth.Realm, "testrealm", "realm")
				assert.Equal(t, auth.Credentials["username"], "testuser", "username")
				assert.Equal(t, auth.Credentials["password"], "testpass", "password")
			},
		},
	})
}
