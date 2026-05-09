package main

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/spacecomputer-io/orbitport/plugins/pkg/core"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/core/health"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/aptosorbital"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/auth"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/authnoop"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/beacon"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/ipfs"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/kms"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/masterseed"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/orbitalimager"
	"github.com/spacecomputer-io/orbitport/plugins/pkg/utils"
	proto "github.com/spacecomputer-io/orbitport/plugins/proto/plugins"
)

func main() {
	utils.GetLogger("orbitport:plugin").Info("Initializing")

	cfg := core.ReadFromEnv()

	logger := utils.GetLogger(fmt.Sprintf("orbitport:plugin:%s", cfg.Plugin))

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(core.UnaryMetricsInterceptor(cfg.Plugin)),
	)

	logger.Infof("Starting plugin %s", cfg.Plugin)

	healthCheck := func(_ context.Context) (health.HealthState, error) {
		return health.HealthStateHealthy, nil
	}

	switch cfg.Plugin {
	case "aptosorbital":
		plugin, err := aptosorbital.NewPlugin()
		if err != nil {
			panic(err)
		}
		proto.RegisterRandomnessPluginServer(grpcServer, plugin)
		logger.Info("Aptos Orbital plugin ready")
	case "auth":
		plugin, err := auth.NewPlugin()
		if err != nil {
			panic(err)
		}
		proto.RegisterAuthPluginServer(grpcServer, plugin)
		logger.Info("Auth plugin ready")
	case "authnoop":
		plugin, err := authnoop.NewPlugin()
		if err != nil {
			panic(err)
		}
		proto.RegisterAuthPluginServer(grpcServer, plugin)
		logger.Info("Noop auth plugin ready")
	case "ipfs":
		plugin, err := ipfs.NewPlugin()
		if err != nil {
			panic(err)
		}
		proto.RegisterIpfsPluginServer(grpcServer, plugin)

		// Override health check for IPFS plugin
		healthCheck = beacon.IpfsHealthCheck

		logger.Info("IPFS plugin ready")
	case "beacon":
		beaconpn, err := beacon.NewPlugin()
		if err != nil {
			panic(err)
		}

		logger.Info("Beacon plugin ready")

		err = beaconpn.Start(context.Background())
		if err != nil {
			logger.Errorf("error starting beacon plugin: %v", err)
		}
		defer func() {
			err := beaconpn.Close()
			if err != nil {
				logger.Errorf("error closing beacon plugin: %v", err)
			}
		}()

	case "masterseed":
		plugin, err := masterseed.NewPlugin()
		if err != nil {
			panic(err)
		}
		proto.RegisterMasterSeedPluginServer(grpcServer, plugin)
		logger.Info("MasterSeed plugin ready")

		defer func() {
			err := plugin.Close()
			if err != nil {
				logger.Errorf("error closing masterseed plugin: %v", err)
			}
		}()
	case "kms":
		plugin, err := kms.NewPlugin()
		if err != nil {
			panic(err)
		}
		proto.RegisterKmsPluginServer(grpcServer, plugin)
		logger.Info("KMS plugin ready")
	case "orbitalimager":
		plugin, err := orbitalimager.NewPlugin()
		if err != nil {
			panic(err)
		}
		proto.RegisterOrbitalImagerPluginServer(grpcServer, plugin)
		logger.Info("OrbitalImager plugin ready")
	default:
		panic("unknown plugin")
	}

	healthServer := health.NewHealthServer(healthCheck)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)

	go func() {
		logger.Infof("Starting metrics server on port %d", cfg.MetricsPort)
		err := core.ListenMetrics(cfg.MetricsPort)
		if err != nil {
			logger.Errorf("failed to start metrics server: %v", err)
		}
	}()

	logger.Infof("Starting gRPC server on port %d", cfg.GrpcPort)

	err := core.ListenGrpc(grpcServer, cfg.GrpcPort)
	if err != nil {
		panic(err)
	}
}
