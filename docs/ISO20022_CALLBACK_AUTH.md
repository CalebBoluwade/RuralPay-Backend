# ISO20022 Callback Authentication

## Overview

The ISO20022 callback endpoints (`/pacs*` and `/acmt*`) now support cryptographic authentication to ensure that inbound callbacks from NIBSS are genuine and have not been tampered with. This document describes the two authentication methods supported: HMAC-SHA256 and mutual TLS (mTLS).

## Endpoints Protected

The following ISO20022 callback endpoints are protected with authentication:

- **Payment Messages (PACS)**
  - `POST /pacs008` - FI-to-FI Customer Credit Transfer
  - `POST /pacs002` - FI-to-FI Payment Status Report
  - `POST /pacs028` - FI-to-FI Payment Status Request

- **Account Messages (ACMT)**
  - `POST /acmt023` - Identification Verification Request
  - `POST /acmt024` - Identification Verification Report

## Authentication Methods

### 1. HMAC-SHA256 (Primary)

HMAC-SHA256 provides signature verification without requiring TLS-level changes.

#### How It Works

1. NIBSS signs the request body using a shared secret with HMAC-SHA256
2. The signature is sent in the `X-Signature` header as `sha256=<hex-encoded-signature>`
3. RuralPay verifies the signature by:
   - Reading the complete request body
   - Computing HMAC-SHA256(body, shared_secret)
   - Comparing with the provided signature using constant-time comparison
   - Rejecting the request if signatures don't match

#### Configuration

Set these environment variables:

```bash
# Enable/disable the authentication requirement (default: true)
ISO20022_CALLBACK_REQUIRE_AUTH=true

# Shared HMAC secret (minimum 32 characters recommended)
ISO20022_CALLBACK_HMAC_SECRET="your-very-long-shared-secret-here"

# Disable MTLS (keep HMAC enabled)
ISO20022_CALLBACK_TLS_ENABLED=false
```

#### NIBSS Integration Example

```bash
#!/bin/bash

# Shared secret (same as ISO20022_CALLBACK_HMAC_SECRET on server)
SHARED_SECRET="your-very-long-shared-secret-here"

# Your request body
BODY='<Document xmlns="urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08">...</Document>'

# Calculate HMAC-SHA256
SIGNATURE=$(echo -n "$BODY" | openssl dgst -sha256 -hmac "$SHARED_SECRET" -hex | sed 's/^.* //')

# Send request to RuralPay
curl -X POST https://api.ruralpay.com/pacs008 \
  -H "Content-Type: application/xml" \
  -H "X-Signature: sha256=$SIGNATURE" \
  -d "$BODY"
```

#### Python Example

```python
import hmac
import hashlib
import requests

SHARED_SECRET = "your-very-long-shared-secret-here"
ENDPOINT = "https://api.ruralpay.com/pacs008"

def send_iso_callback(xml_body):
    # Calculate signature
    signature = hmac.new(
        SHARED_SECRET.encode(),
        xml_body.encode(),
        hashlib.sha256
    ).hexdigest()
    
    headers = {
        "Content-Type": "application/xml",
        "X-Signature": f"sha256={signature}"
    }
    
    response = requests.post(ENDPOINT, data=xml_body, headers=headers)
    return response
```

#### Node.js Example

```javascript
const crypto = require('crypto');
const axios = require('axios');

const SHARED_SECRET = "your-very-long-shared-secret-here";
const ENDPOINT = "https://api.ruralpay.com/pacs008";

async function sendISO20022Callback(xmlBody) {
    const signature = crypto
        .createHmac('sha256', SHARED_SECRET)
        .update(xmlBody)
        .digest('hex');

    const headers = {
        'Content-Type': 'application/xml',
        'X-Signature': `sha256=${signature}`
    };

    const response = await axios.post(ENDPOINT, xmlBody, { headers });
    return response.data;
}
```

### 2. Mutual TLS (mTLS) - Alternative

Mutual TLS provides certificate-based authentication at the transport layer.

#### Configuration

```bash
# Enable mutual TLS verification
ISO20022_CALLBACK_TLS_ENABLED=true

# Optional: Comma-separated list of allowed certificate issuers
ISO20022_CALLBACK_TLS_ALLOWED_ISSUERS="Intermediate CA 1,Intermediate CA 2"

# Optional: Comma-separated list of whitelisted certificate serial numbers
ISO20022_CALLBACK_TLS_WHITELISTED_SERIALS="123456789,987654321"
```

#### Server Setup

Configure your HTTP server to require and verify client certificates:

**Go (with TLS)**

```go
// Configure TLS with client certificate requirement
tlsConfig := &tls.Config{
    ClientAuth: tls.RequireAndVerifyClientCert,
    ClientCAs:  caCertPool,
    // other TLS settings
}

server := &http.Server{
    Addr:      ":8443",
    Handler:   router,
    TLSConfig: tlsConfig,
}

server.ListenAndServeTLS("server.crt", "server.key")
```

**Nginx Reverse Proxy**

```nginx
server {
    listen 443 ssl;
    server_name api.ruralpay.com;

    # Server certificates
    ssl_certificate /etc/ssl/certs/server.crt;
    ssl_certificate_key /etc/ssl/private/server.key;

    # Client certificate verification
    ssl_client_certificate /etc/ssl/certs/nibss-ca.crt;
    ssl_verify_client on;
    ssl_verify_depth 2;

    location ~ ^/(pacs|acmt) {
        # TLS verification happens before reaching Go app
        proxy_pass http://localhost:8080;
    }
}
```

#### NIBSS Integration (mTLS)

NIBSS would provide:
1. Their client certificate signed by a trusted CA
2. Their CA certificate for verification

Your team would:
1. Install NIBSS client certificate for validation
2. Configure the issuer or serial number whitelists
3. Set `ISO20022_CALLBACK_TLS_ENABLED=true`

## Security Best Practices

### HMAC-SHA256

1. **Secret Management**
   - Store `ISO20022_CALLBACK_HMAC_SECRET` in a secure vault
   - Rotate secrets periodically (at least annually)
   - Minimum 32 characters; use cryptographically random values
   - Never commit to version control

2. **Implementation**
   - Always use constant-time comparison (prevents timing attacks)
   - Read entire body before comparison
   - Use HMAC-SHA256, not weaker algorithms
   - Log authentication failures for audit trails

3. **Operational**
   - Monitor and alert on failed authentication attempts
   - Implement rate limiting per source IP
   - Log successful callbacks with source IP and timestamp

### Mutual TLS

1. **Certificate Management**
   - Validate certificate not-before and not-after dates
   - Whitelist by issuer or serial number
   - Implement certificate pinning for high-security requirements
   - Monitor certificate expiration dates

2. **Network**
   - Enforce TLS 1.2 minimum
   - Use strong cipher suites
   - Consider implementing certificate revocation lists (CRL)

## Disabling Authentication (Development Only)

For development or testing without HMAC/mTLS:

```bash
ISO20022_CALLBACK_REQUIRE_AUTH=false
```

⚠️ **WARNING**: Never disable authentication in production. This leaves callbacks vulnerable to spoofing and injection attacks.

## Monitoring and Logging

All authentication events are logged with structured logging:

```
callback.auth.hmac_verified              # Successful HMAC verification
callback.auth.hmac_failed error=...      # Failed HMAC verification
callback.auth.mtls_verified              # Successful mTLS verification
callback.auth.mtls_failed error=...      # Failed mTLS verification
callback.auth.hmac_optional_failed       # Failed (lenient mode)
callback.auth.mtls_optional_failed       # Failed (lenient mode)
```

### Alert Conditions

Set up alerts for:
- High rate of authentication failures from persistent IPs
- Unexpected issuer or serial number in mTLS certificates
- HMAC verification failures (potential tampering)

## Troubleshooting

### "Missing X-Signature header"
- NIBSS is not including the signature header
- Verify NIBSS has been configured with your shared secret
- Check network middleware isn't stripping headers

### "Signature mismatch"
- Shared secrets don't match between NIBSS and RuralPay
- Request body was modified in transit
- NIBSS is using different signing algorithm (ensure SHA256)

### "Client certificate not provided" (mTLS)
- TLS is properly configured but client didn't send certificate
- Verify NIBSS has been configured to send client certificate
- Check certificate file path and permissions

### "Certificate not in allowed list" (mTLS)
- Certificate issuer/serial doesn't match whitelist
- Verify whitelist configuration
- Request NIBSS certificate details and update whitelist

## Configuration Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `ISO20022_CALLBACK_REQUIRE_AUTH` | `true` | Enforce authentication on callbacks |
| `ISO20022_CALLBACK_HMAC_SECRET` | `` | Shared secret for HMAC-SHA256 |
| `ISO20022_CALLBACK_TLS_ENABLED` | `false` | Enable mutual TLS verification |
| `ISO20022_CALLBACK_TLS_ALLOWED_ISSUERS` | `` | Comma-separated allowed CA issuers |
| `ISO20022_CALLBACK_TLS_WHITELISTED_SERIALS` | `` | Comma-separated allowed certificate serials |

## Testing Callback Authentication

### Using curl with HMAC

```bash
BODY='<Document>...</Document>'
SECRET="your-secret"
SIGNATURE=$(echo -n "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | sed 's/^.* //')

curl -X POST http://localhost:8080/pacs008 \
  -H "Content-Type: application/xml" \
  -H "X-Signature: sha256=$SIGNATURE" \
  -d "$BODY"
```

### Using curl with mTLS

```bash
curl -X POST https://localhost:8443/pacs008 \
  --cert client.crt \
  --key client.key \
  --cacert ca.crt \
  -d @message.xml
```

## References

- [HMAC RFC 2104](https://tools.ietf.org/html/rfc2104)
- [Mutual TLS Handshake](https://en.wikipedia.org/wiki/Mutual_authentication#mTLS)
- [ISO 20022 Standard](https://www.iso20022.org/)
