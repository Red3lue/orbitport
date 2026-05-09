package orbitalimager

import "github.com/spf13/viper"

// orbitalImagerConfig is the configuration for the orbitalimager plugin.
//
// Env vars are read by viper with the global ORBITPORT_ prefix configured in
// plugins/pkg/core/config.go, so for example FixturePath is sourced from
// ORBITPORT_ORBITALIMAGER_FIXTURE_PATH.
type orbitalImagerConfig struct {
	// FixturePath is an absolute path to a JPEG/PNG file that the plugin
	// returns on every RequestImagery call. If empty or missing, the plugin
	// generates a small synthetic placeholder so the dev stack still runs.
	FixturePath string
	// Sensor is a free-form identifier embedded in ImageryResult.sensor.
	Sensor string
}

func readFromEnv() *orbitalImagerConfig {
	setDefaults()
	return &orbitalImagerConfig{
		FixturePath: viper.GetString("ORBITALIMAGER_FIXTURE_PATH"),
		Sensor:      viper.GetString("ORBITALIMAGER_SENSOR"),
	}
}

func setDefaults() {
	viper.SetDefault("ORBITALIMAGER_FIXTURE_PATH", "")
	viper.SetDefault("ORBITALIMAGER_SENSOR", "phare-mock-1")
}
