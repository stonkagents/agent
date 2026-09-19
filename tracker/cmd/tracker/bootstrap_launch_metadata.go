// Package: main
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: Wires the MetadataUploader (Pinata or fake) + LaunchMetadataService into
//          the POST /api/launch/metadata handler. Kept out of bootstrap.go to limit
//          merge surface with concurrent launchpad workstreams.

package main

import (
	"fmt"

	"github.com/stonkagents/agent/tracker/internal/api"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// bootstrapLaunchMetadata builds the launch metadata handler from env config.
// Fails fast in production/staging when PINATA_JWT is missing; uses the fake uploader in dev.
func bootstrapLaunchMetadata(env string) (*api.LaunchMetadataHandler, error) {
	mcfg, err := loadMetadataConfig(env)
	if err != nil {
		return nil, fmt.Errorf("launch metadata config: %w", err)
	}
	var uploader services.MetadataUploader
	if mcfg.Uploader == metadataUploaderPinata {
		uploader = services.NewPinataUploader(mcfg.PinataJWT, services.PinataOptions{GatewayURL: mcfg.GatewayURL})
		fmt.Printf("Launch metadata: Pinata uploader (gateway %s, max image %d bytes)\n", mcfg.GatewayURL, mcfg.MaxImageBytes)
	} else {
		uploader = services.NewFakeMetadataUploader(mcfg.GatewayURL)
		fmt.Println("Launch metadata: FAKE uploader (set PINATA_JWT for real IPFS pins)")
	}
	svc := services.NewLaunchMetadataService(uploader, services.LaunchMetadataOptions{
		MaxImageBytes: mcfg.MaxImageBytes, GatewayURL: mcfg.GatewayURL,
	})
	return api.NewLaunchMetadataHandler(svc), nil
}
