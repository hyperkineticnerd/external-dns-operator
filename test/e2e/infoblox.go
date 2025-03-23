//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openshift/external-dns-operator/test/common"

	ibclient "github.com/infobloxopen/infoblox-go-client"

	olmv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1alpha1 "github.com/openshift/external-dns-operator/api/v1alpha1"
	operatorv1beta1 "github.com/openshift/external-dns-operator/api/v1beta1"
)

const (
	infobloxGridConfigDirEnvVar = "INFOBLOX_CONFIG_DIR"
	infobloxGridHostEnvVar      = "INFOBLOX_GRID_HOST"
	infobloxWAPIUsernameEnvVar  = "INFOBLOX_WAPI_USERNAME"
	infobloxWAPIPasswordEnvVar  = "INFOBLOX_WAPI_PASSWORD"
	// infobloxGridMasterHostnameEnvVar can be used in case the grid master hostname
	// is different from grid host (e.g. default "infoblox.localdomain")
	infobloxGridMasterHostnameEnvVar = "INFOBLOX_GRID_MASTER_HOSTNAME"
	trustedCAConfigMapEnvVar         = "TRUSTED_CA_CONFIGMAP_NAME"
	defaultWAPIPort                  = "443"
	// https://community.infoblox.com/cixhp49439/attachments/cixhp49439/IPAM/6153/2/NIOS_8.6.2_ReleaseNotesREVC.pdf
	// Chapter: "Changes to Infoblox API and Restful API (WAPI)"
	// "2.12.2" version is recommended for NIOS 8.6.2 (our Infoblox test instance is currently 8.6.4).
	defaultWAPIVersion            = "2.12.2"
	defaultTLSVerify              = "false"
	defaultHTTPRequestTimeout     = 20
	defaultHTTPConnPool           = 10
	defaultHostFilename           = "host"
	defaultUsernameFilename       = "username"
	defaultPasswordFilename       = "password"
	defaultMasterHostnameFilename = "masterhostname"
	operatorContainerName         = "operator"
)

type infobloxTestHelper struct {
	client             *enhancedIBClient
	gridHost           string
	wapiUsername       string
	wapiPassword       string
	gridMasterHostname string
}

func newInfobloxHelper() (*infobloxTestHelper, error) {
	helper := &infobloxTestHelper{}

	if err := helper.prepareConfigurations(); err != nil {
		return nil, fmt.Errorf("failed to prepare infoblox helper: %w", err)
	}

	hostConfig := ibclient.HostConfig{
		Host:     helper.gridHost,
		Version:  defaultWAPIVersion,
		Port:     defaultWAPIPort,
		Username: helper.wapiUsername,
		Password: helper.wapiPassword,
	}
	transportConfig := ibclient.NewTransportConfig(defaultTLSVerify, defaultHTTPRequestTimeout, defaultHTTPConnPool)

	client, err := newEnhancedIBClient(hostConfig, transportConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to intiantiate enhanced infoblox client: %w", err)
	}
	helper.client = client

	return helper, nil
}

func (h *infobloxTestHelper) ensureHostedZone(zoneDomain string) (string, []string, error) {
	zones := []ibclient.ZoneAuth{}
	err := h.client.GetObject(ibclient.NewZoneAuth(ibclient.ZoneAuth{}), "", &zones)
	if err != nil {
		return "", nil, fmt.Errorf("failed to list authoritative zone: %w", err)
	}
	for _, zone := range zones {
		if zone.Fqdn == zoneDomain {
			return zone.Ref, []string{h.gridHost}, nil
		}
	}

	authZone := ibclient.NewZoneAuth(ibclient.ZoneAuth{Fqdn: zoneDomain})
	ref, err := h.client.CreateObject(authZone)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create authoritative zone: %w", err)
	}
	authZone.Ref = ref

	// NS record is not added automatically with the zone creation
	if err = h.client.addNameServer(authZone.Ref, h.gridMasterHostname); err != nil {
		return "", nil, fmt.Errorf("failed to add nameserver to authoritative zone: %w", err)
	}

	// creation of an authoritative zone needs a restart of the DNS service
	if err = h.client.restartServices(); err != nil {
		return "", nil, fmt.Errorf("failed to restart grid services: %w", err)
	}

	return authZone.Ref, []string{h.gridHost}, nil
}

func (h *infobloxTestHelper) deleteHostedZone(zoneID, zoneDomain string) error {
	if _, err := h.client.DeleteObject(zoneID); err != nil {
		return err
	}

	// deletion of an authoritative zone needs a restart of the DNS service
	if err := h.client.restartServices(); err != nil {
		return err
	}

	return nil
}

func (h *infobloxTestHelper) platform() string {
	return infobloxDNSProvider
}

func (h *infobloxTestHelper) makeCredentialsSecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("infoblox-credentials-%s", randomString(16)),
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"EXTERNAL_DNS_INFOBLOX_WAPI_USERNAME": []byte(h.wapiUsername),
			"EXTERNAL_DNS_INFOBLOX_WAPI_PASSWORD": []byte(h.wapiPassword),
		},
	}
}

func (h *infobloxTestHelper) buildExternalDNS(name, zoneID, zoneDomain string, credsSecret *corev1.Secret) operatorv1beta1.ExternalDNS {
	resource := defaultExternalDNS(name, zoneID, zoneDomain)
	wapiPort, _ := strconv.Atoi(defaultWAPIPort)
	resource.Spec.Provider = operatorv1beta1.ExternalDNSProvider{
		Type: operatorv1beta1.ProviderTypeInfoblox,
		Infoblox: &operatorv1beta1.ExternalDNSInfobloxProviderOptions{
			Credentials: operatorv1beta1.SecretReference{
				Name: credsSecret.Name,
			},
			GridHost:    h.gridHost,
			WAPIPort:    wapiPort,
			WAPIVersion: defaultWAPIVersion,
		},
	}
	return resource
}

func (h *infobloxTestHelper) buildOpenShiftExternalDNS(name, zoneID, zoneDomain, routerName string, credsSecret *corev1.Secret) operatorv1beta1.ExternalDNS {
	resource := routeExternalDNS(name, zoneID, zoneDomain, routerName)
	wapiPort, _ := strconv.Atoi(defaultWAPIPort)
	resource.Spec.Provider = operatorv1beta1.ExternalDNSProvider{
		Type: operatorv1beta1.ProviderTypeInfoblox,
		Infoblox: &operatorv1beta1.ExternalDNSInfobloxProviderOptions{
			Credentials: operatorv1beta1.SecretReference{
				Name: credsSecret.Name,
			},
			GridHost:    h.gridHost,
			WAPIPort:    wapiPort,
			WAPIVersion: defaultWAPIVersion,
		},
	}
	return resource
}

func (h *infobloxTestHelper) buildOpenShiftExternalDNSV1Alpha1(name, zoneID, zoneDomain, routerName string, credsSecret *corev1.Secret) operatorv1alpha1.ExternalDNS {
	resource := routeExternalDNSV1Alpha1(name, zoneID, zoneDomain, routerName)
	wapiPort, _ := strconv.Atoi(defaultWAPIPort)
	resource.Spec.Provider = operatorv1alpha1.ExternalDNSProvider{
		Type: operatorv1alpha1.ProviderTypeInfoblox,
		Infoblox: &operatorv1alpha1.ExternalDNSInfobloxProviderOptions{
			Credentials: operatorv1alpha1.SecretReference{
				Name: credsSecret.Name,
			},
			GridHost:    h.gridHost,
			WAPIPort:    wapiPort,
			WAPIVersion: defaultWAPIVersion,
		},
	}
	return resource
}

func (h *infobloxTestHelper) prepareConfigurations() error {
	configDir := os.Getenv(infobloxGridConfigDirEnvVar)
	if configDir != "" {
		host, err := os.ReadFile(configDir + "/" + defaultHostFilename)
		if err != nil {
			return fmt.Errorf("failed to read grid host from file: %w", err)
		}
		username, err := os.ReadFile(configDir + "/" + defaultUsernameFilename)
		if err != nil {
			return fmt.Errorf("failed to read wapi username from file: %w", err)
		}
		password, err := os.ReadFile(configDir + "/" + defaultPasswordFilename)
		if err != nil {
			return fmt.Errorf("failed to read wapi password from file: %w", err)
		}
		masterHostname, err := os.ReadFile(configDir + "/" + defaultMasterHostnameFilename)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("failed to read grid master hostname from file: %w", err)
			}
			// assume that grid host is a resolvable DNS name
			masterHostname = host
		}
		h.gridHost = string(host)
		h.wapiUsername = string(username)
		h.wapiPassword = string(password)
		h.gridMasterHostname = string(masterHostname)
	} else {
		h.gridHost = common.MustGetEnv(infobloxGridHostEnvVar)
		h.wapiUsername = common.MustGetEnv(infobloxWAPIUsernameEnvVar)
		h.wapiPassword = common.MustGetEnv(infobloxWAPIPasswordEnvVar)
		h.gridMasterHostname = os.Getenv(infobloxGridMasterHostnameEnvVar)
		if h.gridMasterHostname == "" {
			// assume that grid host is a resolvable DNS name
			h.gridMasterHostname = h.gridHost
		}
	}

	// TODO: only needed while we are using the temporary setup of Infoblox.
	// Must be removed once the setup is permanent and has the right certificate.
	return h.trustGridTLSCert()
}

// trustGridTLSCert instructs the operator to trust Grid Master's self signed TLS certificate.
func (h *infobloxTestHelper) trustGridTLSCert() error {
	// get Grid's TLS certificate as raw PEM encoded data
	certRaw, err := readServerTLSCert(net.JoinHostPort(h.gridHost, defaultWAPIPort), true)
	if err != nil {
		return fmt.Errorf("failed to get Grid Master's TLS certificate: %w", err)
	}

	operatorNs := "external-dns-operator"
	trustedCAConfigMapName := fmt.Sprintf("infoblox-trustedca-%s", randomString(16))

	// create a config map with the trusted CA bundle
	trustedCAConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      trustedCAConfigMapName,
			Namespace: operatorNs,
		},
		Data: map[string]string{
			"ca-bundle.crt": string(certRaw),
		},
	}
	if err := common.KubeClient.Create(context.TODO(), trustedCAConfigMap); err != nil {
		return fmt.Errorf("failed to create trusted CA configmap %s/%s: %w", trustedCAConfigMap.Namespace, trustedCAConfigMap.Name, err)
	}

	// trusted CA environment variable to be injected
	trustedCAEnvVar := corev1.EnvVar{
		Name:  trustedCAConfigMapEnvVar,
		Value: trustedCAConfigMapName,
	}

	// inject into subscription if there is one
	findOperatorSubscription := func(ctx context.Context) (*olmv1alpha1.Subscription, error) {
		list := &olmv1alpha1.SubscriptionList{}
		if err := common.KubeClient.List(ctx, list, client.InNamespace(operatorNs)); err != nil {
			return nil, err
		}
		for _, sub := range list.Items {
			if strings.HasPrefix(sub.Name, "external-dns-operator") {
				return &sub, nil
			}
		}
		// CI bundle installation creates a subscription with a generated name which is hard to guess.
		// Doing our best by selecting the first one from the operator namespace.
		if len(list.Items) > 0 {
			return &list.Items[0], nil
		}
		return nil, nil
	}

	if err := wait.PollUntilContextTimeout(context.TODO(), 2*time.Second, 1*time.Minute, true, func(ctx context.Context) (bool, error) {
		subscription, err := findOperatorSubscription(ctx)
		if err != nil {
			fmt.Printf("failed while finding operator subscription: %v, retrying ...\n", err)
			return false, nil
		}
		if subscription != nil {
			if subscription.Spec.Config == nil {
				subscription.Spec.Config = &olmv1alpha1.SubscriptionConfig{}
			}
			subscription.Spec.Config.Env = ensureEnvVar(subscription.Spec.Config.Env, trustedCAEnvVar)
			if err := common.KubeClient.Update(ctx, subscription); err != nil {
				fmt.Printf("failed to inject trusted CA environment variable into the subscription: %v, retrying ...\n", err)
				return false, nil
			}
			return true, nil
		}
		fmt.Println("no suscription was found, trying to update the deployment directly")
		return true, nil
	}); err != nil {
		return fmt.Errorf("timed out trying to inject trusted CA into the subscription")
	}

	findOperatorDeployment := func(ctx context.Context) (*appsv1.Deployment, error) {
		list := &appsv1.DeploymentList{}
		if err := common.KubeClient.List(ctx, list, client.InNamespace(operatorNs)); err != nil {
			return nil, err
		}
		for _, depl := range list.Items {
			if strings.HasPrefix(depl.Name, "external-dns-operator") {
				return &depl, nil
			}
		}
		return nil, nil
	}
	if err := wait.PollUntilContextTimeout(context.TODO(), 2*time.Second, 1*time.Minute, true, func(ctx context.Context) (bool, error) {
		deployment, err := findOperatorDeployment(ctx)
		if err != nil {
			fmt.Printf("failed while finding operator deployment: %v, retrying ...\n", err)
			return false, nil
		}
		if deployment == nil {
			return false, fmt.Errorf("no operator deployment found")
		}

		for i := range deployment.Spec.Template.Spec.Containers {
			if deployment.Spec.Template.Spec.Containers[i].Name == operatorContainerName {
				deployment.Spec.Template.Spec.Containers[i].Env = ensureEnvVar(deployment.Spec.Template.Spec.Containers[i].Env, trustedCAEnvVar)
				break
			}
		}
		if err := common.KubeClient.Update(ctx, deployment); err != nil {
			fmt.Printf("failed to inject trusted CA environment variable into the deployment: %v\n", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed trying to inject trusted CA into the subscription: %w", err)
	}

	return nil
}
