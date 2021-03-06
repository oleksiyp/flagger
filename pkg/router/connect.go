package router

import (
	"fmt"
	consulapi "github.com/hashicorp/consul/api"
	flaggerv1 "github.com/weaveworks/flagger/pkg/apis/flagger/v1beta1"
	clientset "github.com/weaveworks/flagger/pkg/client/clientset/versioned"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
)

// ConsulConnectRouter is managing Consul connect splitters
type ConsulConnectRouter struct {
	kubeClient    kubernetes.Interface
	consulClient  *consulapi.Client
	flaggerClient clientset.Interface
	logger        *zap.SugaredLogger
}

// Reconcile creates or updates the Consul Connect resolver
func (cr *ConsulConnectRouter) Reconcile(canary *flaggerv1.Canary) error {
	err := cr.reconcileResolver(canary)
	if err != nil {
		return err
	}

	err = cr.reconcileSplitter(canary)
	if err != nil {
		return err
	}

	return nil
}

func (cr *ConsulConnectRouter) updateSplitter(canary *flaggerv1.Canary, primaryWeight float32, secondaryWeight float32) error {
	apexName, _, _ := canary.GetServiceNames()

	var splits []consulapi.ServiceSplit

	if primaryWeight > 0.001 {
		splits = append(splits,
			consulapi.ServiceSplit{
				Weight:        primaryWeight,
				Service:       apexName,
				ServiceSubset: "primary",
			})
	}

	if secondaryWeight > 0.001 {
		splits = append(splits,
			consulapi.ServiceSplit{
				Weight:        secondaryWeight,
				Service:       apexName,
				ServiceSubset: "canary",
			})
	}

	splitter := &consulapi.ServiceSplitterConfigEntry{
		Kind:   consulapi.ServiceSplitter,
		Name:   apexName,
		Splits: splits,
	}

	_, _, err := cr.consulClient.ConfigEntries().Set(splitter, nil)
	if err != nil {
		return fmt.Errorf("Not able to set service splitter %s.%s error %w", apexName, canary.Namespace, err)
	}

	return nil
}

func (cr *ConsulConnectRouter) reconcileResolver(canary *flaggerv1.Canary) error {
	apexName, primaryName, _ := canary.GetServiceNames()

	dcs, err := cr.consulClient.Catalog().Datacenters()
	if err != nil {
		cr.logger.Warnf("Failed to fetch dc list %v", err)
		dcs = make([]string, 0)
	}
	if len(dcs) >= 1 {
		dcs = dcs[1:]
	}

	resolver := &consulapi.ServiceResolverConfigEntry{
		Kind:          consulapi.ServiceResolver,
		Name:          apexName,
		DefaultSubset: "primary",
		Subsets: map[string]consulapi.ServiceResolverSubset{
			"primary": {
				Filter: "Service.ID matches \"" + primaryName + "-.+\"",
			},
			"canary": {
				Filter: "Service.ID not matches \"" + primaryName + "-.+\"",
			},
		},
	}

	if len(dcs) > 0 {
		resolver.Failover = make(map[string]consulapi.ServiceResolverFailover)
		resolver.Failover["primary"] = consulapi.ServiceResolverFailover{
			Service:       apexName,
			ServiceSubset: "primary",
			Datacenters:   dcs,
		}

		resolver.Failover["canary"] = consulapi.ServiceResolverFailover{
			Service:       apexName,
			ServiceSubset: "canary",
			Datacenters:   dcs,
		}
	}

	result, _, err := cr.consulClient.ConfigEntries().Set(resolver, nil)
	if err != nil {
		return fmt.Errorf("Failure during creation of service resolver %s.%s error: %w", apexName, canary.Namespace, err)
	}

	if !result {
		return fmt.Errorf("Not able to create service resolver %s.%s", apexName, canary.Namespace)
	}

	return nil
}

func (cr *ConsulConnectRouter) reconcileSplitter(canary *flaggerv1.Canary) error {
	apexName, _, _ := canary.GetServiceNames()

	_, _, err := cr.consulClient.ConfigEntries().Get(consulapi.ServiceSplitter, apexName, nil)
	if err != nil {
		err = cr.updateSplitter(canary, 100.0, 0.0)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetRoutes returns the destinations weight for primary and canary
func (cr *ConsulConnectRouter) GetRoutes(canary *flaggerv1.Canary) (
	primaryWeight int,
	canaryWeight int,
	mirrored bool,
	err error,
) {
	apexName, _, _ := canary.GetServiceNames()

	entry, _, err := cr.consulClient.ConfigEntries().Get(consulapi.ServiceSplitter, apexName, nil)

	if err != nil {
		err = fmt.Errorf("Service splitter %s.%s not found error %w", apexName, canary.Namespace, err)
		return
	}

	readSplitter, ok := entry.(*consulapi.ServiceSplitterConfigEntry)
	if !ok {
		err = fmt.Errorf("Bad service splitter %s.%s", apexName, canary.Namespace)
		return
	}

	for _, split := range readSplitter.Splits {
		if split.ServiceSubset == "primary" {
			primaryWeight = int(split.Weight)
		}
		if split.ServiceSubset == "canary" {
			canaryWeight = int(split.Weight)
		}
	}

	if primaryWeight == 0 && canaryWeight == 0 {
		err = fmt.Errorf("Service splitter %s.%s does not contain routes for %s-primary and %s-canary",
			apexName, canary.Namespace, apexName, apexName)
	}

	return
}

// SetRoutes updates the destinations weight for primary and canary
func (cr *ConsulConnectRouter) SetRoutes(
	canary *flaggerv1.Canary,
	primaryWeight int,
	canaryWeight int,
	mirrored bool,
) error {
	return cr.updateSplitter(canary, float32(primaryWeight), float32(canaryWeight))
}
