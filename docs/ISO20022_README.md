# ISO 20022 Integration with Moov Library - Beginner's Guide

This project integrates the Moov ISO 20022 library to construct transaction payloads for financial messaging standards.

## What is ISO 20022?

ISO 20022 is the **global standard for financial messaging**. Think of it as a common language that banks and financial institutions use to communicate with each other when transferring money.

### Why ISO 20022?
- **Universal**: All major banks worldwide understand this format
- **Structured**: Uses XML format that's both human and machine readable
- **Rich Data**: Can include detailed transaction information
- **Future-proof**: Designed to handle modern payment needs

### Key Concepts for Beginners:

1. **Messages**: Different types of financial communications
   - `pacs.008` = "Please transfer money from A to B"
   - `pacs.002` = "Here's the status of that transfer"

2. **FI to FI**: Financial Institution to Financial Institution (bank-to-bank)

3. **Settlement**: The actual movement of money between banks

4. **XML Format**: Structured data format that looks like HTML but for financial data

## Features

- **pacs.008**: FI to FI Customer Credit Transfer messages
- **pacs.002**: Payment Status Report messages
- XML serialization and deserialization
- Settlement system integration ready

## Dependencies

```bash
go get github.com/moov-io/iso20022
```

## Usage

### Basic Transaction Processing (Step-by-Step for Beginners)

```go
import (
    "github.com/ruralpay/backend/internal/models"
    "github.com/ruralpay/backend/internal/services"
)

// Step 1: Create the ISO 20022 service (this handles all the complex stuff)
iso20022Service := services.NewISO20022Service()

// Step 2: Create a transaction using your existing data structure
tx := &models.Transaction{
    TransactionID: "TXN123456789",  // Your internal transaction ID
    ReferenceID:   "REF987654321",  // Reference for tracking end-to-end
    FromCardID:    "CARD001",       // NFC card that's sending money
    ToCardID:      "CARD002",       // NFC card that's receiving money
    Amount:        250.75,          // Amount in dollars (or your currency)
    Currency:      "NGN",           // Currency code (NGN, EUR, GBP, etc.)
}

// Step 3: Convert your transaction to bank-standard format
// This creates a pacs.008 message ("please transfer money")
pacs008, err := iso20022Service.CreatePacs008(tx)
if err != nil {
    log.Fatal("Failed to create bank message:", err)
}

// Step 4: Convert to XML format that banks understand
xmlData, err := iso20022Service.ConvertToXML(pacs008)
if err != nil {
    log.Fatal("Failed to create XML:", err)
}

// Step 5: Send to the banking system
// (In production, this would go to your bank's API)
err = iso20022Service.SendToSettlement(pacs008)
if err != nil {
    log.Fatal("Failed to send to bank:", err)
}

// Success! Your NFC payment is now in bank-standard format
fmt.Println("Payment sent to banking system!")
```

### Payment Status Reporting (Getting Updates from Banks)

```go
// After you send a pacs.008, the bank will send back a status update
// This creates a pacs.002 message ("here's what happened with your payment")

// Status codes you'll commonly see:
// "ACCP" = Accepted - Bank got your request and will process it
// "ACSC" = Accepted Settlement Completed - Money has been transferred!
// "RJCT" = Rejected - Something went wrong (insufficient funds, invalid account, etc.)
// "PDNG" = Pending - Bank is still working on it

pacs002, err := iso20022Service.CreatePacs002(tx, "ACCP") // ACCP = Accepted
if err != nil {
    log.Fatal("Failed to create status report:", err)
}

// In a real system, you'd receive these messages from the bank
// and use them to update your transaction status in your database
```

## Message Types Explained (For Beginners)

### pacs.008 - FI to FI Customer Credit Transfer
**What it does**: This is like a formal request to transfer money between banks.

**Real-world example**: When you send money from your Chase account to someone's Wells Fargo account, your bank sends a pacs.008 message to Wells Fargo saying "Please credit $100 to account XYZ, we'll settle this later."

**Contains**:
- **Debtor**: Who is sending the money (your bank account)
- **Creditor**: Who is receiving the money (recipient's bank account)
- **Amount**: How much money to transfer
- **Settlement Info**: How and when the banks will exchange the actual money

### pacs.002 - Payment Status Report
**What it does**: This is like a receipt or status update for a payment.

**Real-world example**: After Wells Fargo receives the pacs.008 message, they send back a pacs.002 saying "Got it! We've credited the account" or "Sorry, there was a problem."

**Status codes explained**:
- `ACCP` = **Accepted** - "We got your request and will process it"
- `ACSC` = **Accepted Settlement Completed** - "Done! Money has been transferred"
- `RJCT` = **Rejected** - "Can't do this transfer (insufficient funds, invalid account, etc.)"
- `PDNG` = **Pending** - "We're still working on it"

## HTTP Endpoints

The service provides HTTP endpoints for testing:

- `POST /iso20022/convert` - Convert transaction to ISO 20022 format
- `POST /iso20022/settlement` - Process settlement with status report

## Example Output (Explained for Beginners)

### pacs.008 XML Structure
```xml
<?xml version="1.0" encoding="UTF-8"?>
<!-- This is a request to transfer money between banks -->
<FIToFICstmrCdtTrf>
  <!-- Group Header: Basic info about this message -->
  <GrpHdr>
    <MsgId>unique-message-id</MsgId>           <!-- Unique ID for this message -->
    <CreDtTm>2026-01-03T19:29:43.943477</CreDtTm> <!-- When this message was created -->
    <NbOfTxs>1</NbOfTxs>                        <!-- Number of transactions in this message -->
    <TtlIntrBkSttlmAmt Ccy="NGN">250.75</TtlIntrBkSttlmAmt> <!-- Total amount to settle -->
    <IntrBkSttlmDt>2026-01-03</IntrBkSttlmDt>   <!-- When banks should settle -->
    <SttlmInf>
      <SttlmMtd>CLRG</SttlmMtd>                 <!-- Settlement method: Clearing -->
    </SttlmInf>
  </GrpHdr>
  <!-- Credit Transfer Transaction Info: Details of the actual transfer -->
  <CdtTrfTxInf>
    <PmtId>
      <InstrId>TXN123456789</InstrId>           <!-- Instruction ID (our internal transaction ID) -->
      <EndToEndId>REF987654321</EndToEndId>     <!-- End-to-end reference (tracks from start to finish) -->
      <TxId>TXN123456789</TxId>                 <!-- Transaction ID -->
    </PmtId>
    <IntrBkSttlmAmt Ccy="NGN">250.75</IntrBkSttlmAmt> <!-- Amount for this specific transaction -->
    <IntrBkSttlmDt>2026-01-03</IntrBkSttlmDt>   <!-- Settlement date for this transaction -->
    <ChrgBr>SLEV</ChrgBr>                       <!-- Charge Bearer: Service Level (who pays fees) -->
    <Dbtr>                                      <!-- Debtor: Who is sending the money -->
      <Nm>Debtor Name</Nm>                      <!-- Name of the sender -->
    </Dbtr>
    <Cdtr>                                      <!-- Creditor: Who is receiving the money -->
      <Nm>Creditor Name</Nm>                    <!-- Name of the recipient -->
    </Cdtr>
  </CdtTrfTxInf>
</FIToFICstmrCdtTrf>
```

### What happens in the real world:
1. **Your app** creates this XML message
2. **Your bank** receives it and validates the transaction
3. **Your bank** sends this message to the **recipient's bank**
4. **Recipient's bank** processes it and credits the account
5. **Both banks** settle the actual money transfer later (usually end of day)

## Running the Example

```bash
go run examples/iso20022_example.go
```

## Integration Notes (Beginner-Friendly)

### How this works in your NFC Payment System:

1. **Customer taps NFC card** → Your app processes the payment
2. **Your app creates transaction** → Uses your internal Transaction model
3. **Convert to ISO 20022** → Our service transforms it to bank-standard format
4. **Send to settlement** → Banks can now process the payment
5. **Receive status updates** → Banks send back confirmation/rejection

### Key Benefits:
- **Bank Compatibility**: Any bank that supports ISO 20022 can process your payments
- **Automatic Conversion**: You work with simple Go structs, we handle the complex XML
- **Error Handling**: The Moov library validates all the XML for you
- **Future-Proof**: ISO 20022 is the global standard, won't become obsolete

### What the service does for you:
- ✅ Converts your transaction data to proper ISO format
- ✅ Handles all the complex XML structure
- ✅ Manages proper data types (dates, amounts, currencies)
- ✅ Validates the message format
- ✅ Ready for real bank integration

## Next Steps (Beginner Roadmap)

### Phase 1: Understanding (You are here! 🎉)
- ✅ Learn what ISO 20022 is
- ✅ Understand pacs.008 and pacs.002 messages
- ✅ Run the example code

### Phase 2: Basic Integration
1. **Test with your data**: Replace the example transaction with real data from your NFC system
2. **Connect to a test bank**: Most banks provide sandbox environments for testing
3. **Handle responses**: Process the pacs.002 status messages you receive back

### Phase 3: Production Ready
1. **Real settlement integration**: Replace `SendToSettlement()` with actual bank API calls
2. **Add party details**: Include real debtor/creditor information from your card/account data
3. **Error handling**: Add retry logic, timeout handling, and error recovery
4. **Monitoring**: Log all transactions and their statuses

### Phase 4: Advanced Features
1. **More message types**: 
   - `pain.001` - Customer payment initiation
   - `camt.053` - Bank account statements
   - `camt.054` - Bank notification messages
2. **Bulk processing**: Handle multiple transactions in one message
3. **Real-time status**: Implement webhooks for instant payment confirmations

### Common Beginner Questions:

**Q: Do I need to understand XML?**
A: No! The Moov library handles all XML creation. You just work with Go structs.

**Q: How do I connect to actual banks?**
A: Banks provide APIs or message queues. You'll send the XML we generate to their endpoints.

**Q: What if a payment fails?**
A: The bank sends back a pacs.002 with status "RJCT" and a reason code.

**Q: Is this secure?**
A: Yes, but you'll add TLS encryption and authentication when connecting to real banks.