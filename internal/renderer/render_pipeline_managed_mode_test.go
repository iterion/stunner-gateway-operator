package renderer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/go-logr/logr"
	appv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	// "k8s.io/apimachinery/pkg/types"
	// "sigs.k8s.io/controller-runtime/pkg/log/zap"

	stnrv1a1 "github.com/l7mp/stunner-gateway-operator/api/v1alpha1"
	// stnrconfv1a1 "github.com/l7mp/stunner/pkg/apis/v1alpha1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/l7mp/stunner-gateway-operator/internal/config"
	"github.com/l7mp/stunner-gateway-operator/internal/event"
	"github.com/l7mp/stunner-gateway-operator/internal/store"
	"github.com/l7mp/stunner-gateway-operator/internal/testutils"
	opdefault "github.com/l7mp/stunner-gateway-operator/pkg/config"
)

func TestRenderPipelineManagedMode(t *testing.T) {
	// managed mode
	renderTester(t, []renderTestConfig{
		{
			name: "no EDS - E2E test",
			cls:  []gwapiv1b1.GatewayClass{testutils.TestGwClass},
			cfs:  []stnrv1a1.GatewayConfig{testutils.TestGwConfig},
			gws:  []gwapiv1b1.Gateway{testutils.TestGw},
			rs:   []gwapiv1a2.UDPRoute{testutils.TestUDPRoute},
			svcs: []corev1.Service{testutils.TestSvc},
			dps:  []stnrv1a1.Dataplane{testutils.TestDataplane},
			prep: func(c *renderTestConfig) {
				// update owner ref so that we accept the public IP
				s := testutils.TestSvc.DeepCopy()
				s.SetOwnerReferences([]metav1.OwnerReference{{
					APIVersion: gwapiv1b1.GroupVersion.String(),
					Kind:       "Gateway",
					UID:        testutils.TestGw.GetUID(),
					Name:       testutils.TestGw.GetName(),
				}})
				c.svcs = []corev1.Service{*s}
			},
			tester: func(t *testing.T, r *Renderer) {
				config.DataplaneMode = config.DataplaneModeManaged

				// switch EDS off
				config.EnableEndpointDiscovery = false
				config.EnableRelayToClusterIP = false

				gc, err := r.getGatewayClass()
				assert.NoError(t, err, "gw-class found")
				c := &RenderContext{gc: gc, gws: store.NewGatewayStore(), log: logr.Discard()}
				c.gwConf, err = r.getGatewayConfig4Class(c)
				assert.NoError(t, err, "gw-conf found")
				assert.Equal(t, "gatewayconfig-ok", c.gwConf.GetName(),
					"gatewayconfig name")

				c.update = event.NewEventUpdate(0)
				assert.NotNil(t, c.update, "update event create")

				gws := r.getGateways4Class(c)
				assert.Len(t, gws, 1, "gateways for class")
				gw := gws[0]

				c.gws.ResetGateways([]*gwapiv1b1.Gateway{gw})

				err = r.renderForGateways(c)
				assert.NoError(t, err, "render success")

				// configmap
				cms := c.update.UpsertQueue.ConfigMaps.Objects()
				assert.Len(t, cms, 1, "configmap ready")
				o := cms[0]

				// objectmeta: configmap is now named after gateway!
				assert.Equal(t, o.GetName(), gw.GetName(),
					"configmap name")
				assert.Equal(t, o.GetNamespace(), gw.GetNamespace(),
					"configmap namespace")

				// labels
				labs := o.GetLabels()
				assert.Len(t, labs, 3, "labels len")
				v, ok := labs[opdefault.OwnedByLabelKey]
				assert.True(t, ok, "labels: app")
				assert.Equal(t, opdefault.OwnedByLabelValue, v, "app label value")
				v, ok = labs[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetName(), v, "related-gw label value")
				v, ok = labs[opdefault.RelatedGatewayNamespace]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetNamespace(), v, "related-gw label value")

				// related gw
				as := o.GetAnnotations()
				assert.Len(t, as, 1, "annotations len")
				_, ok = as[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "annotations: related gw")

				cm, ok := o.(*corev1.ConfigMap)
				assert.True(t, ok, "configmap cast")

				conf, err := store.UnpackConfigMap(cm)
				assert.NoError(t, err, "configmap stunner-config unmarshal")

				assert.Equal(t, "testnamespace/gateway-1", conf.Admin.Name, "name")
				assert.Equal(t, testutils.TestLogLevel, conf.Admin.LogLevel,
					"loglevel")

				assert.Equal(t, testutils.TestRealm, conf.Auth.Realm, "realm")
				assert.Equal(t, "plaintext", conf.Auth.Type, "auth-type")
				assert.Equal(t, testutils.TestUsername, conf.Auth.Credentials["username"],
					"username")
				assert.Equal(t, testutils.TestPassword, conf.Auth.Credentials["password"],
					"password")

				assert.Len(t, conf.Listeners, 2, "listener num")
				lc := conf.Listeners[0]
				assert.Equal(t, "testnamespace/gateway-1/gateway-1-listener-udp", lc.Name, "name")
				assert.Equal(t, "TURN-UDP", lc.Protocol, "proto")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Equal(t, int(testutils.TestMinPort), lc.MinRelayPort, "min-port")
				assert.Equal(t, int(testutils.TestMaxPort), lc.MaxRelayPort, "max-port")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				lc = conf.Listeners[1]
				assert.Equal(t, "testnamespace/gateway-1/gateway-1-listener-tcp", lc.Name, "name")
				assert.Equal(t, "TURN-TCP", lc.Protocol, "proto")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Equal(t, int(testutils.TestMinPort), lc.MinRelayPort, "min-port")
				assert.Equal(t, int(testutils.TestMaxPort), lc.MaxRelayPort, "max-port")
				assert.Len(t, lc.Routes, 0, "route num")

				assert.Len(t, conf.Clusters, 1, "cluster num")
				rc := conf.Clusters[0]
				assert.Equal(t, "testnamespace/udproute-ok", rc.Name, "cluster name")
				assert.Equal(t, "STRICT_DNS", rc.Type, "cluster type")
				assert.Len(t, rc.Endpoints, 1, "endpoints len")
				assert.Equal(t, "testservice-ok.testnamespace.svc.cluster.local",
					rc.Endpoints[0], "backend-ref")

				// fmt.Printf("%#v\n", cm.(*corev1.ConfigMap))

				// deployment
				dps := c.update.UpsertQueue.Deployments.Objects()
				assert.Len(t, dps, 1, "deployment num")
				deploy, ok := dps[0].(*appv1.Deployment)
				assert.True(t, ok, "deployment cast")

				assert.Equal(t, gw.GetName(), deploy.GetName(), "deployment name")
				assert.Equal(t, gw.GetNamespace(), deploy.GetNamespace(), "deployment namespace")

				labs = deploy.GetLabels()
				assert.Len(t, labs, 4, "labels len")
				v, ok = labs[opdefault.OwnedByLabelKey]
				assert.True(t, ok, "labels: app")
				assert.Equal(t, opdefault.OwnedByLabelValue, v, "app label value")
				v, ok = labs[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetName(), v, "related-gw label value")
				v, ok = labs[opdefault.RelatedGatewayNamespace]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetNamespace(), v, "related-gw label value")
				// label from the dataplane object
				v, ok = labs["dummy-label"]
				assert.True(t, ok, "labels: dataplane label copied")
				assert.Equal(t, "dummy-value", v, "copied dataplane label value")

				as = deploy.GetAnnotations()
				assert.Len(t, as, 1, "annotations len")
				gwName, ok := as[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "annotations: related gw")
				// annotation is gw-namespace/gw-name
				assert.Equal(t, store.GetObjectKey(gw), gwName, "related-gateway annotation")

				// check the label selector
				labelSelector := deploy.Spec.Selector
				assert.NotNil(t, labelSelector, "label selector")

				selector, err := metav1.LabelSelectorAsSelector(labelSelector)
				assert.NoError(t, err, "label selector convert")

				// match "opdefault.OwnedByLabelKey: opdefault.OwnedByLabelValue" AND
				// "stunner.l7mp.io/related-gateway-name=<gateway-name>"
				labelToMatch := labels.Merge(
					labels.Merge(
						labels.Set{opdefault.AppLabelKey: opdefault.AppLabelValue},
						labels.Set{opdefault.RelatedGatewayKey: gw.GetName()},
					),
					labels.Set{opdefault.RelatedGatewayNamespace: gw.GetNamespace()},
				)
				assert.True(t, selector.Matches(labelToMatch), "selector matched")

				// spec
				assert.NotNil(t, deploy.Spec.Replicas, "replicas notnil")
				assert.Equal(t, int32(3), *deploy.Spec.Replicas, "replicas")
				assert.NotNil(t, deploy.Spec.Strategy, "strategy notnil")

				// pod template spec
				podTemplate := &deploy.Spec.Template
				labs = podTemplate.GetLabels()
				assert.Len(t, labs, 3, "labels len")
				v, ok = labs[opdefault.AppLabelKey]
				assert.True(t, ok, "labels: owned-by")
				assert.Equal(t, opdefault.AppLabelValue, v, "owned-by label value")
				v, ok = labs[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetName(), v, "related-gw label value")
				v, ok = labs[opdefault.RelatedGatewayNamespace]
				assert.True(t, ok, "labels: related namespace")
				assert.Equal(t, gw.GetNamespace(), v, "related-gw-namespace label value")

				// deployment selector matches pod template
				assert.True(t, selector.Matches(labels.Set(labs)), "selector matched")

				podSpec := &podTemplate.Spec

				assert.Len(t, podSpec.Containers, 1, "contianers len")

				container := podSpec.Containers[0]
				assert.Equal(t, opdefault.DefaultStunnerdInstanceName, container.Name, "container 1 name")
				assert.Equal(t, "testimage-1", container.Image, "container 1 image")
				assert.Equal(t, []string{"testcommand-1"}, container.Command, "container 1 command")
				assert.Equal(t, []string{"arg-1", "arg-2"}, container.Args, "container 1 args")

				assert.Equal(t, testutils.TestResourceLimit, container.Resources.Limits, "container 1 - resource limits")
				assert.Equal(t, testutils.TestResourceRequest, container.Resources.Requests, "container 1 - resource req")
				assert.Equal(t, corev1.PullAlways, container.ImagePullPolicy, "container 1 - readiness probe")

				// remainder
				assert.NotNil(t, podSpec.TerminationGracePeriodSeconds, "termination grace ptr")
				assert.Equal(t, testutils.TestTerminationGrace, *podSpec.TerminationGracePeriodSeconds, "termination grace")
				assert.True(t, podSpec.HostNetwork, "hostnetwork")
				assert.Nil(t, podSpec.Affinity, "affinity")

				// gateway status
				assert.Len(t, gw.Status.Conditions, 2, "conditions num")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionAccepted),
					gw.Status.Conditions[0].Type, "conditions accepted")
				assert.Equal(t, int64(0), gw.Status.Conditions[0].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionTrue, gw.Status.Conditions[0].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonAccepted),
					gw.Status.Conditions[0].Reason, "reason")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionProgrammed),
					gw.Status.Conditions[1].Type, "programmed")
				assert.Equal(t, int64(0), gw.Status.Conditions[1].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionTrue, gw.Status.Conditions[1].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonProgrammed),
					gw.Status.Conditions[1].Reason, "reason")

				// route status
				ros := store.UDPRoutes.GetAll()
				assert.Len(t, ros, 1, "routes len")
				ro := ros[0]
				p := ro.Spec.ParentRefs[0]

				assert.Len(t, ro.Status.Parents, 1, "parent status len")
				parentStatus := ro.Status.Parents[0]

				assert.Equal(t, p.Group, parentStatus.ParentRef.Group, "status parent ref group")
				assert.Equal(t, p.Kind, parentStatus.ParentRef.Kind, "status parent ref kind")
				assert.Equal(t, p.Namespace, parentStatus.ParentRef.Namespace, "status parent ref namespace")
				assert.Equal(t, p.Name, parentStatus.ParentRef.Name, "status parent ref name")
				assert.Equal(t, p.SectionName, parentStatus.ParentRef.SectionName, "status parent ref section-name")

				assert.Equal(t, gwapiv1b1.GatewayController("stunner.l7mp.io/gateway-operator"),
					parentStatus.ControllerName, "status parent ref")

				d := meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionAccepted))
				assert.NotNil(t, d, "accepted found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionAccepted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "Accepted", d.Reason, "reason")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionResolvedRefs))
				assert.NotNil(t, d, "resolved-refs found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "ResolvedRefs", d.Reason, "reason")

				// restore EDS
				config.EnableEndpointDiscovery = opdefault.DefaultEnableEndpointDiscovery
				config.EnableRelayToClusterIP = opdefault.DefaultEnableRelayToClusterIP
				config.DataplaneMode = config.NewDataplaneMode(opdefault.DefaultDataplaneMode)
			},
		},
		{
			name: "EDS with relay-to-cluster-IP - E2E test",
			cls:  []gwapiv1b1.GatewayClass{testutils.TestGwClass},
			cfs:  []stnrv1a1.GatewayConfig{testutils.TestGwConfig},
			gws:  []gwapiv1b1.Gateway{testutils.TestGw},
			rs:   []gwapiv1a2.UDPRoute{testutils.TestUDPRoute},
			svcs: []corev1.Service{testutils.TestSvc},
			eps:  []corev1.Endpoints{testutils.TestEndpoint},
			dps:  []stnrv1a1.Dataplane{testutils.TestDataplane},
			prep: func(c *renderTestConfig) {
				s := testutils.TestSvc.DeepCopy()
				s.Spec.ClusterIP = "4.3.2.1"
				// update owner ref so that we accept the public IP
				s.SetOwnerReferences([]metav1.OwnerReference{{
					APIVersion: gwapiv1b1.GroupVersion.String(),
					Kind:       "Gateway",
					UID:        testutils.TestGw.GetUID(),
					Name:       testutils.TestGw.GetName(),
				}})
				c.svcs = []corev1.Service{*s}
			},
			tester: func(t *testing.T, r *Renderer) {
				config.DataplaneMode = config.DataplaneModeManaged

				config.EnableEndpointDiscovery = true
				config.EnableRelayToClusterIP = true

				gc, err := r.getGatewayClass()
				assert.NoError(t, err, "gw-class found")
				c := &RenderContext{gc: gc, gws: store.NewGatewayStore(), log: logr.Discard()}
				c.gwConf, err = r.getGatewayConfig4Class(c)
				assert.NoError(t, err, "gw-conf found")
				assert.Equal(t, "gatewayconfig-ok", c.gwConf.GetName(),
					"gatewayconfig name")

				c.update = event.NewEventUpdate(0)
				assert.NotNil(t, c.update, "update event create")

				gws := r.getGateways4Class(c)
				assert.Len(t, gws, 1, "gateways for class")
				gw := gws[0]

				c.gws.ResetGateways([]*gwapiv1b1.Gateway{gw})

				err = r.renderForGateways(c)
				assert.NoError(t, err, "render success")

				// configmap
				cms := c.update.UpsertQueue.ConfigMaps.Objects()
				assert.Len(t, cms, 1, "configmap ready")
				o := cms[0]

				// objectmeta: configmap is now named after gateway!
				assert.Equal(t, o.GetName(), gw.GetName(),
					"configmap name")
				assert.Equal(t, o.GetNamespace(), gw.GetNamespace(),
					"configmap namespace")

				// related gw
				as := o.GetAnnotations()
				assert.Len(t, as, 1, "annotations len")
				_, ok := as[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "annotations: related gw")

				cm, ok := o.(*corev1.ConfigMap)
				assert.True(t, ok, "configmap cast")

				conf, err := store.UnpackConfigMap(cm)
				assert.NoError(t, err, "configmap stunner-config unmarschal")

				assert.Equal(t, "testnamespace/gateway-1", conf.Admin.Name, "name")
				assert.Equal(t, testutils.TestLogLevel, conf.Admin.LogLevel,
					"loglevel")

				assert.Equal(t, testutils.TestRealm, conf.Auth.Realm, "realm")
				assert.Equal(t, "plaintext", conf.Auth.Type, "auth-type")
				assert.Equal(t, testutils.TestUsername, conf.Auth.Credentials["username"],
					"username")
				assert.Equal(t, testutils.TestPassword, conf.Auth.Credentials["password"],
					"password")

				assert.Len(t, conf.Listeners, 2, "listener num")
				lc := conf.Listeners[0]
				assert.Equal(t, "testnamespace/gateway-1/gateway-1-listener-udp", lc.Name, "name")
				assert.Equal(t, "TURN-UDP", lc.Protocol, "proto")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Equal(t, int(testutils.TestMinPort), lc.MinRelayPort, "min-port")
				assert.Equal(t, int(testutils.TestMaxPort), lc.MaxRelayPort, "max-port")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				lc = conf.Listeners[1]
				assert.Equal(t, "testnamespace/gateway-1/gateway-1-listener-tcp", lc.Name, "name")
				assert.Equal(t, "TURN-TCP", lc.Protocol, "proto")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Equal(t, int(testutils.TestMinPort), lc.MinRelayPort, "min-port")
				assert.Equal(t, int(testutils.TestMaxPort), lc.MaxRelayPort, "max-port")
				assert.Len(t, lc.Routes, 0, "route num")

				assert.Len(t, conf.Clusters, 1, "cluster num")
				rc := conf.Clusters[0]
				assert.Equal(t, "testnamespace/udproute-ok", rc.Name, "cluster name")
				assert.Equal(t, "STATIC", rc.Type, "cluster type")
				assert.Len(t, rc.Endpoints, 5, "endpoints len")
				assert.Contains(t, rc.Endpoints, "1.2.3.4", "endpoint ip-1")
				assert.Contains(t, rc.Endpoints, "1.2.3.5", "endpoint ip-2")
				assert.Contains(t, rc.Endpoints, "1.2.3.6", "endpoint ip-3")
				assert.Contains(t, rc.Endpoints, "1.2.3.7", "endpoint ip-4")
				assert.Contains(t, rc.Endpoints, "4.3.2.1", "cluster-ip")

				// fmt.Printf("%#v\n", cm.(*corev1.ConfigMap))

				// restore EDS
				config.EnableEndpointDiscovery = opdefault.DefaultEnableEndpointDiscovery
				config.EnableRelayToClusterIP = opdefault.DefaultEnableRelayToClusterIP
				config.DataplaneMode = config.NewDataplaneMode(opdefault.DefaultDataplaneMode)
			},
		},
		{
			name: "no EDS  - E2E test - conflicted status",
			cls:  []gwapiv1b1.GatewayClass{testutils.TestGwClass},
			cfs:  []stnrv1a1.GatewayConfig{testutils.TestGwConfig},
			gws:  []gwapiv1b1.Gateway{testutils.TestGw},
			rs:   []gwapiv1a2.UDPRoute{testutils.TestUDPRoute},
			svcs: []corev1.Service{testutils.TestSvc},
			dps:  []stnrv1a1.Dataplane{testutils.TestDataplane},
			prep: func(c *renderTestConfig) {
				gw := testutils.TestGw.DeepCopy()
				gw.Spec.Listeners = []gwapiv1b1.Listener{{
					Name:     gwapiv1b1.SectionName("udp"),
					Port:     gwapiv1b1.PortNumber(1),
					Protocol: gwapiv1b1.ProtocolType("TURN-UDP"),
				}, {
					Name:     gwapiv1b1.SectionName("udp-ok"),
					Port:     gwapiv1b1.PortNumber(2),
					Protocol: gwapiv1b1.ProtocolType("TURN-UDP"),
				}, {
					Name:     gwapiv1b1.SectionName("udp-conflicted"),
					Port:     gwapiv1b1.PortNumber(1),
					Protocol: gwapiv1b1.ProtocolType("TURN-UDP"),
				}, {
					Name:     gwapiv1b1.SectionName("dtls-conflicted"),
					Port:     gwapiv1b1.PortNumber(1),
					Protocol: gwapiv1b1.ProtocolType("TURN-DTLS"),
				}, {
					Name:     gwapiv1b1.SectionName("tcp"),
					Port:     gwapiv1b1.PortNumber(11),
					Protocol: gwapiv1b1.ProtocolType("TURN-TCP"),
				}, {
					Name:     gwapiv1b1.SectionName("tcp-ok"),
					Port:     gwapiv1b1.PortNumber(12),
					Protocol: gwapiv1b1.ProtocolType("TURN-TCP"),
				}, {
					Name:     gwapiv1b1.SectionName("tcp-conflicted"),
					Port:     gwapiv1b1.PortNumber(11),
					Protocol: gwapiv1b1.ProtocolType("TURN-TCP"),
				}, {
					Name:     gwapiv1b1.SectionName("tls-conflicted"),
					Port:     gwapiv1b1.PortNumber(11),
					Protocol: gwapiv1b1.ProtocolType("TURN-TLS"),
				}}
				c.gws = []gwapiv1b1.Gateway{*gw}

				// attach to all listeners
				ro := testutils.TestUDPRoute.DeepCopy()
				ro.Spec.CommonRouteSpec.ParentRefs = []gwapiv1b1.ParentReference{{Name: "gateway-1"}}
				c.rs = []gwapiv1a2.UDPRoute{*ro}

				// update owner ref so that we accept the public IP
				s := testutils.TestSvc.DeepCopy()
				s.SetOwnerReferences([]metav1.OwnerReference{{
					APIVersion: gwapiv1b1.GroupVersion.String(),
					Kind:       "Gateway",
					UID:        testutils.TestGw.GetUID(),
					Name:       testutils.TestGw.GetName(),
				}})
				c.svcs = []corev1.Service{*s}
			},
			tester: func(t *testing.T, r *Renderer) {
				config.DataplaneMode = config.DataplaneModeManaged

				// switch EDS off
				config.EnableEndpointDiscovery = false
				config.EnableRelayToClusterIP = false

				gc, err := r.getGatewayClass()
				assert.NoError(t, err, "gw-class found")
				c := &RenderContext{gc: gc, gws: store.NewGatewayStore(), log: logr.Discard()}
				c.gwConf, err = r.getGatewayConfig4Class(c)
				assert.NoError(t, err, "gw-conf found")
				assert.Equal(t, "gatewayconfig-ok", c.gwConf.GetName(),
					"gatewayconfig name")

				c.update = event.NewEventUpdate(0)
				assert.NotNil(t, c.update, "update event create")

				gws := r.getGateways4Class(c)
				assert.Len(t, gws, 1, "gateways for class")
				gw := gws[0]

				c.gws.ResetGateways([]*gwapiv1b1.Gateway{gw})

				err = r.renderForGateways(c)
				assert.NoError(t, err, "render success")

				// configmap
				cms := c.update.UpsertQueue.ConfigMaps.Objects()
				assert.Len(t, cms, 1, "configmap ready")
				o := cms[0]

				// objectmeta: configmap is now named after gateway!
				assert.Equal(t, o.GetName(), gw.GetName(),
					"configmap name")
				assert.Equal(t, o.GetNamespace(), gw.GetNamespace(),
					"configmap namespace")

				// labels
				labs := o.GetLabels()
				assert.Len(t, labs, 3, "labels len")
				v, ok := labs[opdefault.OwnedByLabelKey]
				assert.True(t, ok, "labels: app")
				assert.Equal(t, opdefault.OwnedByLabelValue, v, "app label value")
				v, ok = labs[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetName(), v, "related-gw label value")
				v, ok = labs[opdefault.RelatedGatewayNamespace]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetNamespace(), v, "related-gw label value")

				// related gw
				as := o.GetAnnotations()
				assert.Len(t, as, 1, "annotations len")
				a, ok := as[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "annotations: related gw")
				assert.Equal(t, store.GetObjectKey(&testutils.TestGw), a, "related-gw annotation ok")

				cm, ok := o.(*corev1.ConfigMap)
				assert.True(t, ok, "configmap cast")

				conf, err := store.UnpackConfigMap(cm)
				assert.NoError(t, err, "configmap stunner-config unmarshal")

				assert.Equal(t, "testnamespace/gateway-1", conf.Admin.Name, "name")
				assert.Equal(t, testutils.TestLogLevel, conf.Admin.LogLevel,
					"loglevel")

				assert.Equal(t, testutils.TestRealm, conf.Auth.Realm, "realm")
				assert.Equal(t, "plaintext", conf.Auth.Type, "auth-type")
				assert.Equal(t, testutils.TestUsername, conf.Auth.Credentials["username"],
					"username")
				assert.Equal(t, testutils.TestPassword, conf.Auth.Credentials["password"],
					"password")

				assert.Len(t, conf.Listeners, 4, "listener num")
				lc := conf.Listeners[0]
				assert.Equal(t, "testnamespace/gateway-1/udp", lc.Name, "name")
				assert.Equal(t, "TURN-UDP", lc.Protocol, "proto")
				assert.Equal(t, 1, lc.Port, "port")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				lc = conf.Listeners[1]
				assert.Equal(t, "testnamespace/gateway-1/udp-ok", lc.Name, "name")
				assert.Equal(t, "TURN-UDP", lc.Protocol, "proto")
				assert.Equal(t, 2, lc.Port, "port")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				lc = conf.Listeners[2]
				assert.Equal(t, "testnamespace/gateway-1/tcp", lc.Name, "name")
				assert.Equal(t, "TURN-TCP", lc.Protocol, "proto")
				assert.Equal(t, 11, lc.Port, "port")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				lc = conf.Listeners[3]
				assert.Equal(t, "testnamespace/gateway-1/tcp-ok", lc.Name, "name")
				assert.Equal(t, "TURN-TCP", lc.Protocol, "proto")
				assert.Equal(t, 12, lc.Port, "port")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				assert.Len(t, conf.Clusters, 1, "cluster num")
				rc := conf.Clusters[0]
				assert.Equal(t, "testnamespace/udproute-ok", rc.Name, "cluster name")
				assert.Equal(t, "STRICT_DNS", rc.Type, "cluster type")
				assert.Len(t, rc.Endpoints, 1, "endpoints len")
				assert.Equal(t, "testservice-ok.testnamespace.svc.cluster.local",
					rc.Endpoints[0], "backend-ref")

				// deployment
				dps := c.update.UpsertQueue.Deployments.Objects()
				assert.Len(t, dps, 1, "deployment num")
				deploy, ok := dps[0].(*appv1.Deployment)
				assert.True(t, ok, "deployment cast")

				assert.Equal(t, gw.GetName(), deploy.GetName(), "deployment name")
				assert.Equal(t, gw.GetNamespace(), deploy.GetNamespace(), "deployment namespace")

				labs = deploy.GetLabels()
				assert.Len(t, labs, 4, "labels len")
				v, ok = labs[opdefault.OwnedByLabelKey]
				assert.True(t, ok, "labels: app")
				assert.Equal(t, opdefault.OwnedByLabelValue, v, "app label value")
				v, ok = labs[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetName(), v, "related-gw label value")
				v, ok = labs[opdefault.RelatedGatewayNamespace]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetNamespace(), v, "related-gw label value")
				// label from the dataplane object
				v, ok = labs["dummy-label"]
				assert.True(t, ok, "labels: dataplane label copied")
				assert.Equal(t, "dummy-value", v, "copied dataplane label value")

				as = deploy.GetAnnotations()
				assert.Len(t, as, 1, "annotations len")
				gwName, ok := as[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "annotations: related gw")
				// annotation is gw-namespace/gw-name
				assert.Equal(t, store.GetObjectKey(gw), gwName, "related-gateway annotation")

				// check the label selector
				labelSelector := deploy.Spec.Selector
				assert.NotNil(t, labelSelector, "label selector")

				selector, err := metav1.LabelSelectorAsSelector(labelSelector)
				assert.NoError(t, err, "label selector convert")

				// match "opdefault.OwnedByLabelKey: opdefault.OwnedByLabelValue" AND
				// "stunner.l7mp.io/related-gateway-name=<gateway-name>"
				labelToMatch := labels.Merge(
					labels.Merge(
						labels.Set{opdefault.AppLabelKey: opdefault.AppLabelValue},
						labels.Set{opdefault.RelatedGatewayKey: gw.GetName()},
					),
					labels.Set{opdefault.RelatedGatewayNamespace: gw.GetNamespace()},
				)
				assert.True(t, selector.Matches(labelToMatch), "selector matched")

				// spec
				assert.NotNil(t, deploy.Spec.Replicas, "replicas notnil")
				assert.Equal(t, int32(3), *deploy.Spec.Replicas, "replicas")
				assert.NotNil(t, deploy.Spec.Strategy, "strategy notnil")

				// pod template spec
				podTemplate := &deploy.Spec.Template
				labs = podTemplate.GetLabels()
				assert.Len(t, labs, 3, "labels len")
				v, ok = labs[opdefault.AppLabelKey]
				assert.True(t, ok, "labels: owned-by")
				assert.Equal(t, opdefault.AppLabelValue, v, "owned-by label value")
				v, ok = labs[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "labels: related")
				assert.Equal(t, gw.GetName(), v, "related-gw label value")
				v, ok = labs[opdefault.RelatedGatewayNamespace]
				assert.True(t, ok, "labels: related namespace")
				assert.Equal(t, gw.GetNamespace(), v, "related-gw-namespace label value")

				// deployment selector matches pod template
				assert.True(t, selector.Matches(labels.Set(labs)), "selector matched")

				podSpec := &podTemplate.Spec

				assert.Len(t, podSpec.Containers, 1, "contianers len")

				container := podSpec.Containers[0]
				assert.Equal(t, opdefault.DefaultStunnerdInstanceName, container.Name, "container 1 name")
				assert.Equal(t, "testimage-1", container.Image, "container 1 image")
				assert.Equal(t, []string{"testcommand-1"}, container.Command, "container 1 command")
				assert.Equal(t, []string{"arg-1", "arg-2"}, container.Args, "container 1 args")

				assert.Equal(t, testutils.TestResourceLimit, container.Resources.Limits, "container 1 - resource limits")
				assert.Equal(t, testutils.TestResourceRequest, container.Resources.Requests, "container 1 - resource req")
				assert.Equal(t, corev1.PullAlways, container.ImagePullPolicy, "container 1 - readiness probe")

				// remainder
				assert.NotNil(t, podSpec.TerminationGracePeriodSeconds, "termination grace ptr")
				assert.Equal(t, testutils.TestTerminationGrace, *podSpec.TerminationGracePeriodSeconds, "termination grace")
				assert.True(t, podSpec.HostNetwork, "hostnetwork")
				assert.Nil(t, podSpec.Affinity, "affinity")

				// gateway status
				assert.Len(t, gw.Status.Conditions, 2, "conditions num")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionAccepted),
					gw.Status.Conditions[0].Type, "conditions accepted")
				assert.Equal(t, int64(0), gw.Status.Conditions[0].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionTrue, gw.Status.Conditions[0].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonAccepted),
					gw.Status.Conditions[0].Reason, "reason")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionProgrammed),
					gw.Status.Conditions[1].Type, "programmed")
				assert.Equal(t, int64(0), gw.Status.Conditions[1].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionTrue, gw.Status.Conditions[1].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonProgrammed),
					gw.Status.Conditions[1].Reason, "reason")

				assert.Len(t, gw.Status.Listeners, 8, "conditions num")

				// listeners[0]: ok
				assert.Equal(t, "udp", string(gw.Status.Listeners[0].Name), "name")
				d := meta.FindStatusCondition(gw.Status.Listeners[0].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonAccepted), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[0].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[0].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonNoConflicts),
					d.Reason, "reason")

				// listeners[1]: ok
				assert.Equal(t, "udp-ok", string(gw.Status.Listeners[1].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[1].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonAccepted), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[1].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[1].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonNoConflicts),
					d.Reason, "reason")

				// listeners[2]: conflict
				assert.Equal(t, "udp-conflicted", string(gw.Status.Listeners[2].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[2].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonPortUnavailable), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[2].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[2].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonProtocolConflict),
					d.Reason, "reason")

				// listeners[3]: conflict
				assert.Equal(t, "dtls-conflicted", string(gw.Status.Listeners[3].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[3].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonPortUnavailable), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[3].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[3].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonProtocolConflict),
					d.Reason, "reason")

				// listeners[4]: ok
				assert.Equal(t, "tcp", string(gw.Status.Listeners[4].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[4].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonAccepted), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[4].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[4].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonNoConflicts),
					d.Reason, "reason")

				// listeners[5]: ok
				assert.Equal(t, "tcp-ok", string(gw.Status.Listeners[5].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[5].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonAccepted), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[5].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[5].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonNoConflicts),
					d.Reason, "reason")

				// listeners[6]: conflict
				assert.Equal(t, "tcp-conflicted", string(gw.Status.Listeners[6].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[6].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonPortUnavailable), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[6].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[6].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonProtocolConflict),
					d.Reason, "reason")

				// listeners[7]: conflict
				assert.Equal(t, "tls-conflicted", string(gw.Status.Listeners[7].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[7].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonPortUnavailable), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[7].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[7].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonProtocolConflict),
					d.Reason, "reason")

				// route status
				ros := store.UDPRoutes.GetAll()
				assert.Len(t, ros, 1, "routes len")
				ro := ros[0]
				p := ro.Spec.ParentRefs[0]

				assert.Len(t, ro.Status.Parents, 1, "parent status len")
				parentStatus := ro.Status.Parents[0]

				assert.Equal(t, p.Group, parentStatus.ParentRef.Group, "status parent ref group")
				assert.Equal(t, p.Kind, parentStatus.ParentRef.Kind, "status parent ref kind")
				assert.Equal(t, p.Namespace, parentStatus.ParentRef.Namespace, "status parent ref namespace")
				assert.Equal(t, p.Name, parentStatus.ParentRef.Name, "status parent ref name")
				assert.Equal(t, p.SectionName, parentStatus.ParentRef.SectionName, "status parent ref section-name")

				assert.Equal(t, gwapiv1b1.GatewayController("stunner.l7mp.io/gateway-operator"),
					parentStatus.ControllerName, "status parent ref")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionAccepted))
				assert.NotNil(t, d, "accepted found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionAccepted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "Accepted", d.Reason, "reason")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionResolvedRefs))
				assert.NotNil(t, d, "resolved-refs found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "ResolvedRefs", d.Reason, "reason")

				// restore EDS
				config.EnableEndpointDiscovery = opdefault.DefaultEnableEndpointDiscovery
				config.EnableRelayToClusterIP = opdefault.DefaultEnableRelayToClusterIP
				config.DataplaneMode = config.NewDataplaneMode(opdefault.DefaultDataplaneMode)
			},
		},
		{
			name: "E2E invalidation",
			cls:  []gwapiv1b1.GatewayClass{testutils.TestGwClass},
			cfs:  []stnrv1a1.GatewayConfig{testutils.TestGwConfig},
			gws:  []gwapiv1b1.Gateway{testutils.TestGw},
			rs:   []gwapiv1a2.UDPRoute{testutils.TestUDPRoute},
			svcs: []corev1.Service{testutils.TestSvc},
			dps:  []stnrv1a1.Dataplane{testutils.TestDataplane},
			prep: func(c *renderTestConfig) {},
			tester: func(t *testing.T, r *Renderer) {
				config.DataplaneMode = config.DataplaneModeManaged

				gc, err := r.getGatewayClass()
				assert.NoError(t, err, "gw-class found")
				c := &RenderContext{gc: gc, gws: store.NewGatewayStore(), log: logr.Discard()}
				c.gwConf, err = r.getGatewayConfig4Class(c)
				assert.NoError(t, err, "gw-conf found")
				assert.Equal(t, "gatewayconfig-ok", c.gwConf.GetName(),
					"gatewayconfig name")

				c.update = event.NewEventUpdate(0)
				assert.NotNil(t, c.update, "update event create")

				gws := r.getGateways4Class(c)
				assert.Len(t, gws, 1, "gateways for class")
				gw := gws[0]

				c.gws.ResetGateways([]*gwapiv1b1.Gateway{gw})

				r.invalidateGatewayClass(c, errors.New("dummy"))

				// configmap
				cms := c.update.UpsertQueue.ConfigMaps.Objects()
				assert.Len(t, cms, 1, "configmap ready")
				o := cms[0]

				// objectmeta: configmap is now named after gateway!
				assert.Equal(t, o.GetName(), gw.GetName(),
					"configmap name")
				assert.Equal(t, o.GetNamespace(), gw.GetNamespace(),
					"configmap namespace")

				// related gw
				as := o.GetAnnotations()
				assert.Len(t, as, 1, "annotations len")
				_, ok := as[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "annotations: related gw")

				cm, ok := o.(*corev1.ConfigMap)
				assert.True(t, ok, "configmap cast")

				conf, found := cm.Data[opdefault.DefaultStunnerdConfigfileName]
				assert.True(t, found, "configmap data: stunnerd.conf found")
				assert.Equal(t, "", conf, "configmap data: stunnerd.conf empty")

				// fmt.Printf("%#v\n", cm.(*corev1.ConfigMap))

				//statuses
				setGatewayClassStatusAccepted(gc, nil)
				assert.Len(t, gc.Status.Conditions, 1, "conditions num")
				assert.Equal(t, string(gwapiv1b1.GatewayClassConditionStatusAccepted),
					gc.Status.Conditions[0].Type, "conditions accepted")
				assert.Equal(t, metav1.ConditionTrue,
					gc.Status.Conditions[0].Status, "conditions status")
				assert.Equal(t, string(gwapiv1b1.GatewayClassReasonAccepted),
					gc.Status.Conditions[0].Type, "conditions reason")
				assert.Equal(t, int64(0),
					gc.Status.Conditions[0].ObservedGeneration, "conditions gen")

				objs := c.update.UpsertQueue.Gateways.Objects()
				assert.Len(t, gws, 1, "gateway num")
				gw, found = objs[0].(*gwapiv1b1.Gateway)
				assert.True(t, found, "gateway found")
				assert.Equal(t, fmt.Sprintf("%s/%s", testutils.TestNsName, "gateway-1"),
					store.GetObjectKey(gw), "gw name found")

				assert.Len(t, gw.Status.Conditions, 2, "conditions num")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionAccepted),
					gw.Status.Conditions[0].Type, "conditions accepted")
				assert.Equal(t, int64(0), gw.Status.Conditions[0].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionTrue, gw.Status.Conditions[0].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonAccepted),
					gw.Status.Conditions[0].Reason, "reason")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionProgrammed),
					gw.Status.Conditions[1].Type, "programmed")
				assert.Equal(t, int64(0), gw.Status.Conditions[1].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionFalse, gw.Status.Conditions[1].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonInvalid),
					gw.Status.Conditions[1].Reason, "reason")
			},
		},
		{
			name: "EDS with no relay-to-cluster-IP - E2E rendering for multiple gateway-classes",
			cls:  []gwapiv1b1.GatewayClass{testutils.TestGwClass},
			cfs:  []stnrv1a1.GatewayConfig{testutils.TestGwConfig},
			gws:  []gwapiv1b1.Gateway{testutils.TestGw},
			rs:   []gwapiv1a2.UDPRoute{testutils.TestUDPRoute},
			svcs: []corev1.Service{testutils.TestSvc},
			eps:  []corev1.Endpoints{testutils.TestEndpoint},
			dps:  []stnrv1a1.Dataplane{testutils.TestDataplane},
			prep: func(c *renderTestConfig) {
				// a new gatewayclass that specifies a different gateway-config
				// a new gatewayclass that specifies a different gateway-config
				dummyGc := testutils.TestGwClass.DeepCopy()
				dummyGc.SetName("dummy-gateway-class")
				dummyGc.Spec.ParametersRef = &gwapiv1b1.ParametersReference{
					Group:     gwapiv1b1.Group(stnrv1a1.GroupVersion.Group),
					Kind:      gwapiv1b1.Kind("GatewayConfig"),
					Name:      "dummy-gateway-config",
					Namespace: &testutils.TestNsName,
				}
				c.cls = []gwapiv1b1.GatewayClass{testutils.TestGwClass, *dummyGc}

				// the new gateway-config that renders into a different stunner configmap
				dummyConf := testutils.TestGwConfig.DeepCopy()
				dummyConf.SetName("dummy-gateway-config")
				target := "dummy-stunner-config"
				dummyConf.Spec.StunnerConfig = &target
				c.cfs = []stnrv1a1.GatewayConfig{testutils.TestGwConfig, *dummyConf}

				// a new gateway whose controller-name is the new gatewayclass
				dummyGw := testutils.TestGw.DeepCopy()
				dummyGw.SetName("dummy-gateway")
				dummyGw.Spec.GatewayClassName =
					gwapiv1b1.ObjectName("dummy-gateway-class")
				c.gws = []gwapiv1b1.Gateway{*dummyGw, testutils.TestGw}

				// a route for dummy-gateway
				sn := gwapiv1b1.SectionName("gateway-1-listener-udp")
				udpRoute := testutils.TestUDPRoute.DeepCopy()
				udpRoute.Spec.CommonRouteSpec.ParentRefs = []gwapiv1b1.ParentReference{{
					Name:      "dummy-gateway",
					Namespace: &testutils.TestNsName,
				}, {
					Name:        gwapiv1b1.ObjectName(testutils.TestGw.GetName()),
					Namespace:   &testutils.TestNsName,
					SectionName: &sn,
				}}
				udpRoute.Spec.Rules[0].BackendRefs = []gwapiv1b1.BackendRef{{
					BackendObjectReference: gwapiv1b1.BackendObjectReference{
						Name:      gwapiv1b1.ObjectName(testutils.TestSvc.GetName()),
						Namespace: &testutils.TestNsName,
					},
				}, {
					BackendObjectReference: gwapiv1b1.BackendObjectReference{
						Name:      "dummy-service",
						Namespace: &testutils.TestNsName,
					},
				}}
				c.rs = []gwapiv1a2.UDPRoute{*udpRoute}

				s := testutils.TestSvc.DeepCopy()
				s.Spec.ClusterIP = "4.3.2.1"
				// update owner ref so that we accept the public IP
				s.SetOwnerReferences([]metav1.OwnerReference{{
					APIVersion: gwapiv1b1.GroupVersion.String(),
					Kind:       "Gateway",
					UID:        testutils.TestGw.GetUID(),
					Name:       testutils.TestGw.GetName(),
				}})
				dummySvc := testutils.TestSvc.DeepCopy()
				dummySvc.SetName("dummy-service")
				c.svcs = []corev1.Service{*s, *dummySvc}

				dummyEp := testutils.TestEndpoint.DeepCopy()
				dummyEp.SetName("dummy-service")
				dummyEp.Subsets = []corev1.EndpointSubset{{
					Addresses:         []corev1.EndpointAddress{{IP: "4.4.4.4"}},
					NotReadyAddresses: []corev1.EndpointAddress{{}},
				}}
				c.eps = []corev1.Endpoints{testutils.TestEndpoint, *dummyEp}
			},
			tester: func(t *testing.T, r *Renderer) {
				config.DataplaneMode = config.DataplaneModeManaged

				config.EnableEndpointDiscovery = true
				config.EnableRelayToClusterIP = false

				gcs := r.getGatewayClasses()
				assert.Len(t, gcs, 2, "gw-classes found")

				// original config
				gc := gcs[0]
				// we can never know the order...
				if gc.GetName() == "dummy-gateway-class" {
					gc = gcs[1]
				}

				assert.Equal(t, "gatewayclass-ok", gc.GetName(),
					"gatewayclass name")

				var err error
				c := &RenderContext{gc: gc, gws: store.NewGatewayStore(), log: logr.Discard()}
				c.gwConf, err = r.getGatewayConfig4Class(c)
				assert.NoError(t, err, "gw-conf found")
				assert.Equal(t, "gatewayconfig-ok", c.gwConf.GetName(),
					"gatewayconfig name")

				c.update = event.NewEventUpdate(0)
				assert.NotNil(t, c.update, "update event create")

				gws := r.getGateways4Class(c)
				assert.Len(t, gws, 1, "gateways for class")

				// render first gw
				gw := gws[0]
				c.gws.ResetGateways([]*gwapiv1b1.Gateway{gw})

				err = r.renderForGateways(c)
				assert.NoError(t, err, "render success")

				// configmap
				cms := c.update.UpsertQueue.ConfigMaps.Objects()
				assert.Len(t, cms, 1, "configmap ready")

				o := cms[0]

				// objectmeta: configmap is now named after gateway!
				assert.Equal(t, o.GetName(), gw.GetName(),
					"configmap name")
				assert.Equal(t, o.GetNamespace(), gw.GetNamespace(),
					"configmap namespace")

				// related gw
				as := o.GetAnnotations()
				assert.Len(t, as, 1, "annotations len")
				_, ok := as[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "annotations: related gw")

				cm, ok := o.(*corev1.ConfigMap)
				assert.True(t, ok, "configmap cast")

				conf, err := store.UnpackConfigMap(cm)
				assert.NoError(t, err, "configmap stunner-config unmarschal")

				assert.Equal(t, "testnamespace/gateway-1", conf.Admin.Name, "name")
				assert.Equal(t, testutils.TestLogLevel, conf.Admin.LogLevel,
					"loglevel")

				assert.Equal(t, testutils.TestRealm, conf.Auth.Realm, "realm")
				assert.Equal(t, "plaintext", conf.Auth.Type, "auth-type")
				assert.Equal(t, testutils.TestUsername, conf.Auth.Credentials["username"],
					"username")
				assert.Equal(t, testutils.TestPassword, conf.Auth.Credentials["password"],
					"password")

				assert.Len(t, conf.Listeners, 2, "listener num")
				lc := conf.Listeners[0]
				assert.Equal(t, "testnamespace/gateway-1/gateway-1-listener-udp", lc.Name, "name")
				assert.Equal(t, "TURN-UDP", lc.Protocol, "proto")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Equal(t, int(testutils.TestMinPort), lc.MinRelayPort, "min-port")
				assert.Equal(t, int(testutils.TestMaxPort), lc.MaxRelayPort, "max-port")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				lc = conf.Listeners[1]
				assert.Equal(t, "testnamespace/gateway-1/gateway-1-listener-tcp", lc.Name, "name")
				assert.Equal(t, "TURN-TCP", lc.Protocol, "proto")
				assert.Equal(t, "1.2.3.4", lc.PublicAddr, "public-ip")
				assert.Equal(t, int(testutils.TestMinPort), lc.MinRelayPort, "min-port")
				assert.Equal(t, int(testutils.TestMaxPort), lc.MaxRelayPort, "max-port")
				assert.Len(t, lc.Routes, 0, "route num")

				assert.Len(t, conf.Clusters, 1, "cluster num")
				rc := conf.Clusters[0]
				assert.Equal(t, "testnamespace/udproute-ok", rc.Name, "cluster name")
				assert.Equal(t, "STATIC", rc.Type, "cluster type")
				assert.Len(t, rc.Endpoints, 5, "endpoints len")
				assert.Contains(t, rc.Endpoints, "1.2.3.4", "endpoint ip-1")
				assert.Contains(t, rc.Endpoints, "1.2.3.5", "endpoint ip-2")
				assert.Contains(t, rc.Endpoints, "1.2.3.6", "endpoint ip-3")
				assert.Contains(t, rc.Endpoints, "1.2.3.7", "endpoint ip-4")
				assert.Contains(t, rc.Endpoints, "4.4.4.4", "endpoint ip-5")

				// gateway status
				assert.Len(t, gw.Status.Conditions, 2, "conditions num")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionAccepted),
					gw.Status.Conditions[0].Type, "conditions accepted")
				assert.Equal(t, int64(0), gw.Status.Conditions[0].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionTrue, gw.Status.Conditions[0].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonAccepted),
					gw.Status.Conditions[0].Reason, "reason")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionProgrammed),
					gw.Status.Conditions[1].Type, "programmed")
				assert.Equal(t, int64(0), gw.Status.Conditions[1].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionTrue, gw.Status.Conditions[1].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonProgrammed),
					gw.Status.Conditions[1].Reason, "reason")

				assert.Len(t, gw.Status.Listeners, 3, "conditions num")

				// listeners[0]: ok
				assert.Equal(t, "gateway-1-listener-udp", string(gw.Status.Listeners[0].Name), "name")
				d := meta.FindStatusCondition(gw.Status.Listeners[0].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonAccepted), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[0].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[0].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonNoConflicts),
					d.Reason, "reason")

				// listeners[1]: invalid
				assert.Equal(t, "invalid", string(gw.Status.Listeners[1].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[1].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "accepted found")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonUnsupportedProtocol), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[1].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[1].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonNoConflicts),
					d.Reason, "reason")

				// listeners[2]: ok
				assert.Equal(t, "gateway-1-listener-tcp", string(gw.Status.Listeners[2].Name), "name")
				d = meta.FindStatusCondition(gw.Status.Listeners[2].Conditions,
					string(gwapiv1b1.ListenerConditionAccepted))
				assert.NotNil(t, d, "acceptedfound")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonAccepted), d.Reason,
					"reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[2].Conditions,
					string(gwapiv1b1.ListenerConditionResolvedRefs))
				assert.NotNil(t, d, "resovedrefs found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonResolvedRefs),
					d.Reason, "reason")

				d = meta.FindStatusCondition(gw.Status.Listeners[2].Conditions,
					string(gwapiv1b1.ListenerConditionConflicted))
				assert.NotNil(t, d, "conflicted found")
				assert.Equal(t, string(gwapiv1b1.ListenerConditionConflicted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionFalse, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, string(gwapiv1b1.ListenerReasonNoConflicts),
					d.Reason, "reason")

				// route status
				ros := store.UDPRoutes.GetAll()
				assert.Len(t, ros, 1, "routes len")
				ro := ros[0]

				assert.Len(t, ro.Status.Parents, 2, "parent status len")
				parentStatus := ro.Status.Parents[0]
				p := ro.Spec.ParentRefs[0]

				assert.Equal(t, p.Group, parentStatus.ParentRef.Group, "status parent ref group")
				assert.Equal(t, p.Kind, parentStatus.ParentRef.Kind, "status parent ref kind")
				assert.Equal(t, p.Namespace, parentStatus.ParentRef.Namespace, "status parent ref namespace")
				assert.Equal(t, p.Name, parentStatus.ParentRef.Name, "status parent ref name")
				assert.Equal(t, p.SectionName, parentStatus.ParentRef.SectionName, "status parent ref section-name")

				assert.Equal(t, gwapiv1b1.GatewayController("stunner.l7mp.io/gateway-operator"),
					parentStatus.ControllerName, "status parent ref")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionAccepted))
				assert.NotNil(t, d, "accepted found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionAccepted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "Accepted", d.Reason, "reason")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionResolvedRefs))
				assert.NotNil(t, d, "resolved-refs found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "ResolvedRefs", d.Reason, "reason")

				parentStatus = ro.Status.Parents[1]
				p = ro.Spec.ParentRefs[1]

				assert.Equal(t, p.Group, parentStatus.ParentRef.Group, "status parent ref group")
				assert.Equal(t, p.Kind, parentStatus.ParentRef.Kind, "status parent ref kind")
				assert.Equal(t, p.Namespace, parentStatus.ParentRef.Namespace, "status parent ref namespace")
				assert.Equal(t, p.Name, parentStatus.ParentRef.Name, "status parent ref name")
				assert.Equal(t, p.SectionName, parentStatus.ParentRef.SectionName, "status parent ref section-name")

				assert.Equal(t, gwapiv1b1.GatewayController("stunner.l7mp.io/gateway-operator"),
					parentStatus.ControllerName, "status parent ref")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionAccepted))
				assert.NotNil(t, d, "accepted found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionAccepted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "Accepted", d.Reason, "reason")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionResolvedRefs))
				assert.NotNil(t, d, "resolved-refs found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "ResolvedRefs", d.Reason, "reason")

				// fmt.Printf("%#v\n", cm.(*corev1.ConfigMap))

				// config for the modified gateway-class
				gc = gcs[1]
				// we can never know the order...
				if gc.GetName() != "dummy-gateway-class" {
					gc = gcs[0]
				}
				assert.Equal(t, "dummy-gateway-class", gc.GetName(),
					"gatewayclass name")

				c = &RenderContext{gc: gc, gws: store.NewGatewayStore(), log: logr.Discard()}
				c.gwConf, err = r.getGatewayConfig4Class(c)
				assert.NoError(t, err, "gw-conf found")
				assert.Equal(t, "dummy-gateway-config", c.gwConf.GetName(),
					"gatewayconfig name")

				c.update = event.NewEventUpdate(0)
				assert.NotNil(t, c.update, "update event create")

				gws = r.getGateways4Class(c)
				assert.Len(t, gws, 1, "gateways for class")

				// render gw
				gw = gws[0]
				c.gws.ResetGateways([]*gwapiv1b1.Gateway{gw})

				err = r.renderForGateways(c)
				assert.NoError(t, err, "render success")

				// configmap
				cms = c.update.UpsertQueue.ConfigMaps.Objects()
				assert.Len(t, cms, 1, "configmap ready")

				o = cms[0]

				// objectmeta: configmap is now named after gateway!
				assert.Equal(t, o.GetName(), gw.GetName(),
					"configmap name")
				assert.Equal(t, o.GetNamespace(), gw.GetNamespace(),
					"configmap namespace")

				// related gw
				as = o.GetAnnotations()
				assert.Len(t, as, 1, "annotations len")
				_, ok = as[opdefault.RelatedGatewayKey]
				assert.True(t, ok, "annotations: related gw")

				cm, ok = o.(*corev1.ConfigMap)
				assert.True(t, ok, "configmap cast")

				conf, err = store.UnpackConfigMap(cm)
				assert.NoError(t, err, "configmap stunner-config unmarschal")

				assert.Equal(t, "testnamespace/dummy-gateway", conf.Admin.Name, "name")
				assert.Equal(t, testutils.TestLogLevel, conf.Admin.LogLevel,
					"loglevel")

				assert.Equal(t, testutils.TestRealm, conf.Auth.Realm, "realm")
				assert.Equal(t, "plaintext", conf.Auth.Type, "auth-type")
				assert.Equal(t, testutils.TestUsername, conf.Auth.Credentials["username"],
					"username")
				assert.Equal(t, testutils.TestPassword, conf.Auth.Credentials["password"],
					"password")

				assert.Len(t, conf.Listeners, 2, "listener num")
				lc = conf.Listeners[0]
				assert.Equal(t, "testnamespace/dummy-gateway/gateway-1-listener-udp", lc.Name, "name")
				assert.Equal(t, "TURN-UDP", lc.Protocol, "proto")
				// the service links to the original gateway, our gateway does not
				// have linkage, so public addr should be empty
				assert.Equal(t, "", lc.PublicAddr, "public-ip")
				assert.Equal(t, int(testutils.TestMinPort), lc.MinRelayPort, "min-port")
				assert.Equal(t, int(testutils.TestMaxPort), lc.MaxRelayPort, "max-port")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				lc = conf.Listeners[1]
				assert.Equal(t, "testnamespace/dummy-gateway/gateway-1-listener-tcp", lc.Name, "name")
				assert.Equal(t, "TURN-TCP", lc.Protocol, "proto")
				assert.Equal(t, "", lc.PublicAddr, "public-ip")
				assert.Equal(t, int(testutils.TestMinPort), lc.MinRelayPort, "min-port")
				assert.Equal(t, int(testutils.TestMaxPort), lc.MaxRelayPort, "max-port")
				assert.Len(t, lc.Routes, 1, "route num")
				assert.Equal(t, lc.Routes[0], "testnamespace/udproute-ok", "udp route")

				assert.Len(t, conf.Clusters, 1, "cluster num")
				rc = conf.Clusters[0]
				assert.Equal(t, "testnamespace/udproute-ok", rc.Name, "cluster name")
				assert.Equal(t, "STATIC", rc.Type, "cluster type")
				assert.Len(t, rc.Endpoints, 5, "endpoints len")
				assert.Contains(t, rc.Endpoints, "1.2.3.4", "endpoint ip-1")
				assert.Contains(t, rc.Endpoints, "1.2.3.5", "endpoint ip-2")
				assert.Contains(t, rc.Endpoints, "1.2.3.6", "endpoint ip-3")
				assert.Contains(t, rc.Endpoints, "1.2.3.7", "endpoint ip-4")
				assert.Contains(t, rc.Endpoints, "4.4.4.4", "endpoint ip-5")

				// fmt.Printf("%#v\n", cm.(*corev1.ConfigMap))

				// gateway status
				assert.Len(t, gw.Status.Conditions, 2, "conditions num")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionAccepted),
					gw.Status.Conditions[0].Type, "conditions accepted")
				assert.Equal(t, int64(0), gw.Status.Conditions[0].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionTrue, gw.Status.Conditions[0].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonAccepted),
					gw.Status.Conditions[0].Reason, "reason")

				assert.Equal(t, string(gwapiv1b1.GatewayConditionProgrammed),
					gw.Status.Conditions[1].Type, "programmed")
				assert.Equal(t, int64(0), gw.Status.Conditions[1].ObservedGeneration,
					"conditions gen")
				assert.Equal(t, metav1.ConditionFalse, gw.Status.Conditions[1].Status,
					"status")
				assert.Equal(t, string(gwapiv1b1.GatewayReasonAddressNotAssigned),
					gw.Status.Conditions[1].Reason, "reason")

				// route status
				assert.Len(t, ro.Status.Parents, 2, "parent status len")
				parentStatus = ro.Status.Parents[0]
				p = ro.Spec.ParentRefs[0]

				assert.Equal(t, p.Group, parentStatus.ParentRef.Group, "status parent ref group")
				assert.Equal(t, p.Kind, parentStatus.ParentRef.Kind, "status parent ref kind")
				assert.Equal(t, p.Namespace, parentStatus.ParentRef.Namespace, "status parent ref namespace")
				assert.Equal(t, p.Name, parentStatus.ParentRef.Name, "status parent ref name")
				assert.Equal(t, p.SectionName, parentStatus.ParentRef.SectionName, "status parent ref section-name")

				assert.Equal(t, gwapiv1b1.GatewayController("stunner.l7mp.io/gateway-operator"),
					parentStatus.ControllerName, "status parent ref")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionAccepted))
				assert.NotNil(t, d, "accepted found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionAccepted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "Accepted", d.Reason, "reason")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionResolvedRefs))
				assert.NotNil(t, d, "resolved-refs found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "ResolvedRefs", d.Reason, "reason")

				parentStatus = ro.Status.Parents[1]
				p = ro.Spec.ParentRefs[1]

				assert.Equal(t, p.Group, parentStatus.ParentRef.Group, "status parent ref group")
				assert.Equal(t, p.Kind, parentStatus.ParentRef.Kind, "status parent ref kind")
				assert.Equal(t, p.Namespace, parentStatus.ParentRef.Namespace, "status parent ref namespace")
				assert.Equal(t, p.Name, parentStatus.ParentRef.Name, "status parent ref name")
				assert.Equal(t, p.SectionName, parentStatus.ParentRef.SectionName, "status parent ref section-name")

				assert.Equal(t, gwapiv1b1.GatewayController("stunner.l7mp.io/gateway-operator"),
					parentStatus.ControllerName, "status parent ref")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionAccepted))
				assert.NotNil(t, d, "accepted found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionAccepted), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "Accepted", d.Reason, "reason")

				d = meta.FindStatusCondition(parentStatus.Conditions,
					string(gwapiv1b1.RouteConditionResolvedRefs))
				assert.NotNil(t, d, "resolved-refs found")
				assert.Equal(t, string(gwapiv1b1.RouteConditionResolvedRefs), d.Type,
					"type")
				assert.Equal(t, metav1.ConditionTrue, d.Status, "status")
				assert.Equal(t, int64(0), d.ObservedGeneration, "gen")
				assert.Equal(t, "ResolvedRefs", d.Reason, "reason")

				// restore EDS
				config.EnableEndpointDiscovery = opdefault.DefaultEnableEndpointDiscovery
				config.EnableRelayToClusterIP = opdefault.DefaultEnableRelayToClusterIP
				config.DataplaneMode = config.NewDataplaneMode(opdefault.DefaultDataplaneMode)
			},
		},
	})
}
