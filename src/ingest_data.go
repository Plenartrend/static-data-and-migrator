package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	dip "plenartrend/static-data-and-migrator/src/openAPI"
)

func ingestData() {
	// Create a client with the DIP API server URL
	// Option 1: Use NewClientWithResponses for parsed responses (recommended)
	client, err := dip.NewClientWithResponses(
		"https://search.dip.bundestag.de/api/v1",
		dip.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			// Add API key via header (Authorization: ApiKey YOUR_KEY)
			req.Header.Set("Authorization", fmt.Sprintf("ApiKey %s", os.Getenv("DIP_API_Key")))
			return nil
		}),
	)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Example: Get a list of Vorgänge (legislative processes)
	ctx := context.Background()
	resp, err := client.GetVorgangListWithResponse(ctx, &dip.GetVorgangListParams{})
	if err != nil {
		log.Fatalf("API request failed: %v", err)
	}

	// Check the response
	if resp.StatusCode() != http.StatusOK {
		log.Fatalf("Unexpected status: %d", resp.StatusCode())
	}

	// Access the parsed JSON response
	if resp.JSON200 != nil {
		log.Printf("Found %d documents\n", resp.JSON200.NumFound)
		for _, v := range resp.JSON200.Documents {
			abstract := ""
			if v.Abstract != nil {
				abstract = *v.Abstract
			}
			log.Printf("- %s: %s\n", v.Id, abstract)
		}
	}
}

// Helper for creating pointers
func ptr[T any](v T) *T {
	return &v
}
