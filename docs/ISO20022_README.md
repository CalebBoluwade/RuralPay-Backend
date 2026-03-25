# ISO 20022 Integration

RuralPay uses the [Moov ISO 20022](https://github.com/moov-io/iso20022) library to construct and parse financial messages exchanged with NIBSS. All outbound messages are signed and encrypted using a hybrid RSA + AES-256-GCM scheme before being dispatched. Inbound callbacks are verified and decrypted before parsing.

---

## Message Types

| Message | Direction | Purpose |
|---|---|---|
| `pacs.008.001.08` | Outbound | FI-to-FI credit transfer — initiates a funds transfer to NIBSS |
| `pacs.002.001.08` | Inbound / Outbound | Payment status report — NIBSS confirms, rejects, or reports status |
| `pacs.028.001.04` | Outbound | Payment status request — query NIBSS for the status of a prior transfer |
| `acmt.023.001.02` | Outbound | Identification verification request — verify an account number at a bank |
| `acmt.024.001.02` | Inbound | Identification verification report — NIBSS response to acmt.023 |

---

## Message Security

Every outbound message goes through a hybrid encryption + signing pipeline before it reaches NIBSS. The implementation lives in `internal/utils/crypto.go` and is wired into `ISO20022Service`.

### How it works

```
XML payload
    │
    ▼
SealMessage(xml, senderPriv, nibssPub)
    │
    ├─ 1. Generate random 32-byte AES key
    ├─ 2. Encrypt XML with AES-256-GCM          → EncryptedPayload (nonce prepended)
    ├─ 3. Wrap AES key with nibss_public.key     → WrappedKey  (only NIBSS can unwrap)
    └─ 4. Sign EncryptedPayload with iso20022_signing.key → Signature (RSA-PSS + SHA-256)
    │
    ▼
SignedMessage { EncryptedPayload, WrappedKey, Signature }
    │
    ▼
JSON → HTTP POST to NIBSS
```

NIBSS receives the `SignedMessage` JSON and:
1. Verifies `Signature` using `iso20022_signing_public.key` — confirms the message came from RuralPay
2. Unwraps `WrappedKey` using their private key — recovers the AES key
3. Decrypts `EncryptedPayload` — reads the XML

If signature verification fails, the message is rejected before any decryption is attempted.

### Inbound callbacks

Callback handlers (`ISO20022CallbackHandler`) call `decryptBody` on every request. It:
1. Reads the raw body
2. Attempts to JSON-decode it as a `SignedMessage`
3. If successful, calls `VerifyAndOpenXML` to verify and decrypt
4. Falls back to treating the body as plain XML if it is not a `SignedMessage` (backward compatible)

---

## Key Setup

### Keys on disk

```
keys/
├── iso20022_signing.key          # RuralPay RSA-2048 private key  (never share)
├── iso20022_signing_public.key   # RuralPay public key            (share with NIBSS)
├── nibss_public.key              # NIBSS public key               (received from NIBSS)
└── nibss_private.key             # NIBSS private key              (TEST ONLY — delete in production)
```

`nibss_private.key` was generated locally to simulate NIBSS in tests. It must not exist in production — NIBSS generates and holds their own private key.

### Generating keys

```bash
# RuralPay signing keypair
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out keys/iso20022_signing.key
openssl pkey -in keys/iso20022_signing.key -pubout -out keys/iso20022_signing_public.key

# Simulated NIBSS keypair (tests only)
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out keys/nibss_private.key
openssl pkey -in keys/nibss_private.key -pubout -out keys/nibss_public.key
```

In production, replace `keys/nibss_public.key` with the PEM file received from NIBSS during onboarding.

### Environment variables

```env
ISO20022_SIGNING_KEY_PATH=keys/iso20022_signing.key
ISO20022_NIBSS_PUB_KEY_PATH=keys/nibss_public.key
```

Keys are loaded at startup in `NewISO20022Service`. If either path is missing or unreadable, the service starts without signing — messages are sent as plain XML. This is intentional for local development without keys configured.

---

## HTTP Endpoints

### Outbound (service-initiated)

| Method | Path | Description |
|---|---|---|
| `POST` | `/iso20022/convert` | Convert a transaction to pacs.008 XML and return it |
| `POST` | `/iso20022/settlement` | Build, sign, and send a pacs.008 to NIBSS; returns pacs.002 result |

### Inbound callbacks (NIBSS-initiated)

| Method | Path | Handler | Message |
|---|---|---|---|
| `POST` | `/pacs008` | `ReceivePacs008` | Inbound credit transfer from NIBSS |
| `POST` | `/pacs002` | `ReceivePacs002` | Payment status report from NIBSS |
| `POST` | `/pacs028` | `ReceivePacs028` | Payment status request from NIBSS |
| `POST` | `/acmt023` | `ReceiveAcmt023` | Account identification verification request |
| `POST` | `/acmt024` | `ReceiveAcmt024` | Account identification verification report |

All callback endpoints accept either a `SignedMessage` JSON envelope or raw XML.

---

## Usage

### Send a funds transfer

```go
svc := services.NewISO20022Service()

tx := &models.TransactionRecord{
    TransactionID: "TXN123456789",
    FromAccountID: "0123456789",
    ToAccountID:   "9876543210",
    ToBankCode:    "000013",
    Amount:        25075, // kobo (250.75 NGN)
    Currency:      "NGN",
}

// Builds pacs.008, signs + encrypts it, sends to NIBSS, returns pacs.002 result
result, err := svc.SendToSettlement(pacs008)
// result.Status: "ACSC" | "ACCP" | "RJCT"
// result.RejectReason: populated when Status == "RJCT"
```

### Query payment status

```go
result, err := svc.RequestPaymentStatus(originalMsgID, originalTxID)
```

Builds a pacs.028, sends it to NIBSS, and returns the pacs.002 response.

### Verify an account

```go
doc, err := svc.CreateAcmt023("0123456789", "000013")
xmlData, err := svc.ConvertToXML(doc)
resp, err := svc.nibssClient.VerifyAccountIdentification([]byte(xmlData))
// resp.Verified, resp.AccountName
```

### Sign and verify manually

```go
// Seal
msg, err := svc.SignXML(xmlString)
// msg.EncryptedPayload, msg.WrappedKey, msg.Signature

// Open
plaintext, err := svc.VerifyAndOpenXML(msg)
```

---

## pacs.002 Status Codes

| Code | Meaning |
|---|---|
| `ACCP` | Accepted — NIBSS received and will process |
| `ACSC` | Accepted Settlement Completed — funds transferred |
| `RJCT` | Rejected — see `RejectReason` for the ISO reason code |
| `PDNG` | Pending — still processing |

`SendToSettlement` returns an error for any status other than `ACSC` or `ACCP`.

---

## Example pacs.008 XML

```xml
<?xml version="1.0" encoding="UTF-8"?>
<FIToFICstmrCdtTrf>
  <GrpHdr>
    <MsgId>550e8400-e29b-41d4-a716-446655440000</MsgId>
    <CreDtTm>2026-01-03T19:29:43</CreDtTm>
    <NbOfTxs>1</NbOfTxs>
    <TtlIntrBkSttlmAmt Ccy="NGN">250.75</TtlIntrBkSttlmAmt>
    <IntrBkSttlmDt>2026-01-03</IntrBkSttlmDt>
    <SttlmInf>
      <SttlmMtd>CLRG</SttlmMtd>
    </SttlmInf>
  </GrpHdr>
  <CdtTrfTxInf>
    <PmtId>
      <InstrId>TXN123456789</InstrId>
      <EndToEndId>TXN123456789</EndToEndId>
      <TxId>TXN123456789</TxId>
    </PmtId>
    <IntrBkSttlmAmt Ccy="NGN">250.75</IntrBkSttlmAmt>
    <ChrgBr>SLEV</ChrgBr>
    <DbtrAgt>
      <FinInstnId><BICFI>RURALPAY</BICFI></FinInstnId>
    </DbtrAgt>
    <Dbtr><Nm>0123456789</Nm></Dbtr>
    <CdtrAgt>
      <FinInstnId>
        <ClrSysMmbId><MmbId>000013</MmbId></ClrSysMmbId>
      </FinInstnId>
    </CdtrAgt>
    <Cdtr><Nm>9876543210</Nm></Cdtr>
  </CdtTrfTxInf>
</FIToFICstmrCdtTrf>
```

---

## Dependencies

```
github.com/moov-io/iso20022   — ISO 20022 message structs and XML marshalling
golang.org/x/crypto           — RSA-PSS signing, AES-256-GCM encryption
```
