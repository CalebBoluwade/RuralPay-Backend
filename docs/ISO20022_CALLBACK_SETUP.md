# ISO20022 Callback Authentication Setup Guide

## Quick Start

### 1. Generate a Shared Secret

```bash
# Generate a secure random secret
openssl rand -hex 32
# Output: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1
```

### 2. Configure Environment Variables

Create a `.env` file with:

```env
# ISO20022 Callback Authentication
ISO20022_CALLBACK_REQUIRE_AUTH=true
ISO20022_CALLBACK_HMAC_SECRET=a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1
ISO20022_CALLBACK_TLS_ENABLED=false
```

### 3. Test the Callback Endpoint

Save a test XML message to `test_pacs008.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08">
  <CstmrCdtTrfInitn>
    <GrpHdr>
      <MsgId>TEST20240403001</MsgId>
      <CreDtTm>2024-04-03T15:30:00Z</CreDtTm>
      <NbOfTxs>1</NbOfTxs>
      <TtlIntrBkSttlmAmt Ccy="NGN">100000.00</TtlIntrBkSttlmAmt>
    </GrpHdr>
    <PmtInf>
      <PmtInfId>PINFO001</PmtInfId>
      <PmtMtd>TRA</PmtMtd>
    </PmtInf>
  </CstmrCdtTrfInitn>
</Document>
```

Send with HMAC signature:

```bash
#!/bin/bash

SHARED_SECRET="a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1"
BODY=$(cat test_pacs008.xml)
SIGNATURE=$(echo -n "$BODY" | openssl dgst -sha256 -hmac "$SHARED_SECRET" -hex | sed 's/^.* //')

curl -v -X POST http://localhost:8080/pacs008 \
  -H "Content-Type: application/xml" \
  -H "X-Signature: sha256=$SIGNATURE" \
  -d "$BODY"
```

Expected response:
```json
{
  "message": "pacs.008 received",
  "data": {
    "messageType": "pacs.008.001.08",
    "msgId": "TEST20240403001",
    "nbOfTxs": 1
  },
  "timestamp": "2024-04-03T15:30:00Z"
}
```

## Advanced: Using OpenSSL for Signature Generation

```bash
# Using dgst command (most portable)
echo -n "message body" | openssl dgst -sha256 -hmac "secret" -hex

# Using mac command (newer OpenSSL 1.1.1+)
echo -n "message body" | openssl mac -cipher sha256 -macopt key:"secret" -hex

# Using HMAC in Python
python3 -c "
import hmac, hashlib
body = b'message body'
secret = b'secret'
print(hmac.new(secret, body, hashlib.sha256).hexdigest())
"
```

## Production Deployment Checklist

### Security

- [ ] Use strong HMAC secret (32+ bytes of random data)
- [ ] Store secret in secure vault (AWS Secrets Manager, HashiCorp Vault, etc.)
- [ ] Rotate secret annually or when team member leaves
- [ ] Enable `ISO20022_CALLBACK_REQUIRE_AUTH=true` in production
- [ ] Monitor authentication failure rates
- [ ] Set up alerts for repeated authentication failures

### Configuration

- [ ] Set appropriate log levels for debugging
- [ ] Configure centralized logging for audit trail
- [ ] Set up monitoring for callback endpoint latency
- [ ] Document secret rotation procedure

### Testing

- [ ] Create integration tests with NIBSS staging environment
- [ ] Test both successful and failed authentication scenarios
- [ ] Verify error responses are appropriate
- [ ] Test with various message sizes and encodings

### Documentation

- [ ] Share authentication details securely with NIBSS
- [ ] Document callback URL with port/protocol
- [ ] Provide example request with signature
- [ ] Document expected response format
- [ ] Create runbook for troubleshooting

## Example: NIBSS Integration

Provide NIBSS with:

```
Endpoint: https://api.ruralpay.com/pacs008
Authentication: HMAC-SHA256
Signature Header: X-Signature
Format: X-Signature: sha256=<hex-encoded-signature>
Content-Type: application/xml

Contact: [Your ops contact]
Support Email: [Your support email]
```

## Switching from No Auth to HMAC Auth

To minimize disruption:

1. **Phase 1**: Deploy with `ISO20022_CALLBACK_REQUIRE_AUTH=false` (no restrictions)
2. **Phase 2**: Distribute shared secret to NIBSS, test integration
3. **Phase 3**: Deploy middleware in optional/audit mode with monitoring
4. **Phase 4**: After 2 weeks with no errors, set `ISO20022_CALLBACK_REQUIRE_AUTH=true`
5. **Phase 5**: Monitor for issues, keep on-call support available

## Rotating HMAC Secret

To change the shared secret:

1. Generate new secret
2. Deploy app with both old and new secrets (if middleware supports it)
3. Communicate new secret to NIBSS
4. Wait for NIBSS to update and test
5. Monitor callback success rate
6. Disable old secret once confirmed

*Note: Current implementation only supports one secret. For seamless rotation, implement:*

```go
// Accept calls signed with either current or previous secret
verifyedWithCurrent := hmac.Equal(expectedCurrent, providedSignature)
verifiedWithPrevious := hmac.Equal(expectedPrevious, providedSignature)
if !verifiedWithCurrent && !verifiedWithPrevious {
    return fmt.Errorf("signature mismatch")
}
```

## Monitoring Dashboard Queries

### Successful Callbacks (if using structured logging)

```
level=INFO message="callback.auth.hmac_verified"
```

### Failed Callbacks

```
level=WARN message="callback.auth.hmac_failed"
```

### By Endpoint

```
callback.pacs008.processed
callback.pacs002.processed
callback.pacs028.processed
callback.acmt023.processed
callback.acmt024.processed
```

## Troubleshooting Guide

| Issue | Cause | Solution |
|-------|-------|----------|
| "Missing X-Signature header" | Header not sent | Verify NIBSS config |
| "Signature mismatch" | Wrong secret or body changed | Compare secrets, check network |
| "Invalid X-Signature format" | Wrong header format | Must be `sha256=<hex>` |
| 401 Unauthorized repeatedly | Persistent secret mismatch | Redeploy shared secret |
| High latency | Signature verification overhead | Negligible; check network |

## References

- [RFC 2104 - HMAC](https://tools.ietf.org/html/rfc2104)
- [OpenSSL dgst Documentation](https://www.openssl.org/docs/man1.1.1/man1/openssl-dgst.html)
- [Crypto Timing Attacks](https://en.wikipedia.org/wiki/Timing_attack)
