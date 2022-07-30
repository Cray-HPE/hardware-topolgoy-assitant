/*
Copyright © 2022 NAME HERE <EMAIL ADDRESS>

*/
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Cray-HPE/cray-site-init/pkg/csi"
	sls_client "github.com/Cray-HPE/hms-sls/pkg/sls-client"
	sls_common "github.com/Cray-HPE/hms-sls/pkg/sls-common"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.hpe.com/sjostrand/topology-tool/internal/engine"
	"github.hpe.com/sjostrand/topology-tool/pkg/ccj"
	"github.hpe.com/sjostrand/topology-tool/pkg/configs"
	"gopkg.in/yaml.v2"
)

// updateCmd represents the update command
var updateCmd = &cobra.Command{
	Use:   "update [CCJ_FILE]",
	Args:  cobra.ExactArgs(1),
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Initialize the global viper
		v := viper.GetViper()
		v.BindPFlags(cmd.Flags())

		// Setup Context
		ctx := setupContext()

		// Setup HTTP client
		httpClient := retryablehttp.NewClient()

		// Setup SLS client
		currentSLSStateLocation := v.GetString("sls-current-state")
		var slsClient *sls_client.SLSClient
		if strings.HasPrefix(currentSLSStateLocation, "http") {
			slsClient = sls_client.NewSLSClient(currentSLSStateLocation, httpClient.StandardClient(), "")
		}

		//
		// Parse input files
		//
		ccjFile := args[0]
		fmt.Printf("Using CCJ file at %s\n", ccjFile)

		// Read in the paddle file
		paddleRaw, err := ioutil.ReadFile(ccjFile)
		if err != nil {
			panic(err)
		}

		var paddle ccj.Paddle
		if err := json.Unmarshal(paddleRaw, &paddle); err != nil {
			panic(err)
		}

		// TODO Verify Paddle
		// - Check CANU Version?
		// - Check Architecture against list of supported

		supportedArchitectures := map[string]bool{
			"network_v2_tds": true,
			"network_v2":     true,
		}
		if !supportedArchitectures[paddle.Architecture] {
			err := fmt.Errorf("unsupported paddle architecture (%v)", paddle.Architecture)
			panic(err)
		}

		// Read in cabinet lookup
		cabinetLookupFile := v.GetString("cabinet-lookup")
		fmt.Printf("Using cabinet lookup file at %s\n", cabinetLookupFile)
		cabinetLookupRaw, err := ioutil.ReadFile(cabinetLookupFile)
		if err != nil {
			panic(err)
		}

		var cabinetLookup configs.CabinetLookup
		if err := yaml.Unmarshal(cabinetLookupRaw, &cabinetLookup); err != nil {
			panic(err)
		}

		// Read in application_node_config.yaml
		// TODO the prefixes list is not being used, as we are assuming all unknown nodes are application
		applicationNodeConfigFile := v.GetString("application-node-config")
		fmt.Printf("Using application node config file at %s\n", applicationNodeConfigFile)
		applicationNodeRaw, err := ioutil.ReadFile(applicationNodeConfigFile)
		if err != nil {
			panic(err) // TODO
		}

		var applicationNodeConfig csi.SLSGeneratorApplicationNodeConfig
		if err := yaml.Unmarshal(applicationNodeRaw, &applicationNodeConfig); err != nil {
			panic(err) // TODO
		}
		if err := applicationNodeConfig.Normalize(); err != nil {
			panic(err) // TODO
		}
		if err := applicationNodeConfig.Validate(); err != nil {
			panic(err) // TODO
		}

		//
		// Retrieve current state from the system
		//
		fmt.Printf("Using current SLS state at %s\n", currentSLSStateLocation)

		var currentSLSState sls_common.SLSState
		if strings.HasPrefix(currentSLSStateLocation, "http") {
			currentSLSState, err = slsClient.GetDumpState(ctx)
			if err != nil {
				// TODO give a better error message
				panic(err)
			}
		} else {
			currentSLSStateRaw, err := ioutil.ReadFile(currentSLSStateLocation)
			if err != nil {
				panic(err) // TODO
			}

			if err := json.Unmarshal(currentSLSStateRaw, &currentSLSState); err != nil {
				panic(err) // TODO
			}
		}

		// TODO HACK reading from file

		//
		// Determine topology changes
		//
		topologyEngine := engine.TopologyEngine{
			Input: engine.EngineInput{
				Paddle:                paddle,
				CabinetLookup:         cabinetLookup,
				ApplicationNodeConfig: applicationNodeConfig,
				CurrentSLSState:       currentSLSState,
			},
		}

		topologyChanges, err := topologyEngine.DetermineChanges()
		if err != nil {
			panic(err)
		}

		{
			//
			// Debug stuff
			//
			topologyChangesRaw, err := json.MarshalIndent(topologyChanges, "", "  ")
			if err != nil {
				panic(err)
			}

			ioutil.WriteFile("topology_changes.json", topologyChangesRaw, 0600)
		}

		//
		// Determine changes requires to downstream services from SLS. Like HSM and BSS
		//

		//
		// Perform changes to SLS/HSM/BSS on the system to reflect the updated state.
		//

		// Add new hardware
		fmt.Println("Adding new hardware to SLS")
		for _, hardware := range topologyChanges.HardwareAdded {
			if slsClient != nil {
				slsClient.PutHardware(ctx, hardware)
			}
		}

		// Update modified networks
		fmt.Println("Updating modified networks in SLS")
		for _, modifiedNetwork := range topologyChanges.ModifiedNetworks {
			if slsClient != nil {
				slsClient.PutNetwork(ctx, modifiedNetwork)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// updateCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// updateCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")

	// TODO hack should point to a SLS service
	// TODO Would be cool if this could work with both HTTP(s) with a SLS service, and
	// locally with a SLS state file
	updateCmd.Flags().String("sls-current-state", "http://localhost:8376", "Location of the current SLS state")

	updateCmd.Flags().String("cabinet-lookup", "cabinet_lookup.yaml", "YAML containing extra cabinet metadata")
	updateCmd.Flags().String("application-node-config", "application_node_config.yaml", "YAML to control Application node identification during the SLS State generation")
}

func setupContext() context.Context {
	var cancel context.CancelFunc
	ctx, cancel := context.WithCancel(context.Background())

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-c

		// Cancel the context to cancel any in progress HTTP requests.
		cancel()
	}()

	return ctx
}
