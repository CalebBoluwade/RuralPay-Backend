package main

import (
	"encoding/xml"
	"fmt"
	"log"

	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/services"
)

func main() {
	// Create ISO20022 service
	iso20022Service := services.NewISO20022Service()

	// Example transaction
	tx := &models.Transaction{
		TransactionID: "TXN123456789",
		ReferenceID:   "REF987654321",
		FromAccountID: "CARD001",
		ToAccountID:   "CARD002",
		Amount:        250.75,
		Currency:      "NGN",
		Status:        "PENDING",
	}

	fmt.Println("=== Creating pacs.008 (FI to FI Customer Credit Transfer) ===")

	// Create pacs.008 message
	pacs008, err := iso20022Service.CreatePacs008(tx)
	if err != nil {
		log.Fatalf("Failed to create pacs.008: %v", err)
	}

	// Convert to XML
	xmlData, err := xml.MarshalIndent(pacs008, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal XML: %v", err)
	}

	fmt.Printf("pacs.008 XML:\n%s\n\n", xml.Header+string(xmlData))

	fmt.Println("=== Creating pacs.002 (Payment Status Report) ===")

	// Create pacs.002 status report
	pacs002, err := iso20022Service.CreatePacs002(tx, "ACCP")
	if err != nil {
		log.Fatalf("Failed to create pacs.002: %v", err)
	}

	// Convert to XML
	xmlData2, err := xml.MarshalIndent(pacs002, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal XML: %v", err)
	}

	fmt.Printf("pacs.002 XML:\n%s\n\n", xml.Header+string(xmlData2))

	fmt.Println("=== Sending to Settlement ===")

	// Send to settlement system
	err = iso20022Service.SendToSettlement(pacs008)
	if err != nil {
		log.Fatalf("Failed to send to settlement: %v", err)
	}

	fmt.Println("Transaction successfully processed and sent to settlement!")
}
