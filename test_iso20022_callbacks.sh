#!/bin/bash
# ISO20022 Callback Authentication Test Suite
# This script tests HMAC signature verification for ISO20022 callbacks
# Usage: ./test_iso20022_callbacks.sh [server_url] [hmac_secret]

set -e

# Configuration
SERVER_URL="${1:-http://localhost:8080}"
HMAC_SECRET="${2:-test-secret-key}"
ENDPOINT="${SERVER_URL}/pacs008"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}=== ISO20022 Callback Authentication Tests ===${NC}"
echo "Server: $SERVER_URL"
echo "Endpoint: $ENDPOINT"
echo ""

# Test data - valid pacs.008 message
VALID_MESSAGE='<?xml version="1.0" encoding="UTF-8"?>
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
</Document>'

# Function to calculate HMAC-SHA256
calculate_hmac() {
    local body="$1"
    local secret="$2"
    echo -n "$body" | openssl dgst -sha256 -hmac "$secret" -hex | sed 's/^.* //'
}

# Function to print test result
print_result() {
    local test_name="$1"
    local http_code="$2"
    local expected_code="$3"
    
    if [ "$http_code" == "$expected_code" ]; then
        echo -e "${GREEN}✓ PASS${NC}: $test_name (HTTP $http_code)"
        return 0
    else
        echo -e "${RED}✗ FAIL${NC}: $test_name (Expected HTTP $expected_code, got $http_code)"
        return 1
    fi
}

# Test 1: Valid request with correct HMAC
echo -e "${YELLOW}Test 1: Valid request with correct HMAC${NC}"
SIGNATURE=$(calculate_hmac "$VALID_MESSAGE" "$HMAC_SECRET")
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$ENDPOINT" \
    -H "Content-Type: application/xml" \
    -H "X-Signature: sha256=$SIGNATURE" \
    -d "$VALID_MESSAGE")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)
print_result "Valid HMAC signature" "$HTTP_CODE" "200"
echo "Response: $BODY"
echo ""

# Test 2: Missing X-Signature header
echo -e "${YELLOW}Test 2: Missing X-Signature header${NC}"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$ENDPOINT" \
    -H "Content-Type: application/xml" \
    -d "$VALID_MESSAGE")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)
print_result "Missing X-Signature header" "$HTTP_CODE" "401"
echo "Response: $BODY"
echo ""

# Test 3: Invalid HMAC signature
echo -e "${YELLOW}Test 3: Invalid HMAC signature${NC}"
INVALID_SIGNATURE="sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$ENDPOINT" \
    -H "Content-Type: application/xml" \
    -H "X-Signature: $INVALID_SIGNATURE" \
    -d "$VALID_MESSAGE")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)
print_result "Invalid HMAC signature" "$HTTP_CODE" "401"
echo "Response: $BODY"
echo ""

# Test 4: Wrong secret (produces different valid-format signature)
echo -e "${YELLOW}Test 4: Wrong secret${NC}"
WRONG_SECRET="wrong-secret-key"
WRONG_SIGNATURE=$(calculate_hmac "$VALID_MESSAGE" "$WRONG_SECRET")
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$ENDPOINT" \
    -H "Content-Type: application/xml" \
    -H "X-Signature: sha256=$WRONG_SIGNATURE" \
    -d "$VALID_MESSAGE")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)
print_result "Wrong HMAC secret" "$HTTP_CODE" "401"
echo "Response: $BODY"
echo ""

# Test 5: Modified message body
echo -e "${YELLOW}Test 5: Modified message body${NC}"
SIGNATURE=$(calculate_hmac "$VALID_MESSAGE" "$HMAC_SECRET")
MODIFIED_MESSAGE=$(echo "$VALID_MESSAGE" | sed 's/100000/200000/')
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$ENDPOINT" \
    -H "Content-Type: application/xml" \
    -H "X-Signature: sha256=$SIGNATURE" \
    -d "$MODIFIED_MESSAGE")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)
print_result "Modified message body" "$HTTP_CODE" "401"
echo "Response: $BODY"
echo ""

# Test 6: Invalid X-Signature format (no equals sign)
echo -e "${YELLOW}Test 6: Invalid X-Signature format (no equals)${NC}"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$ENDPOINT" \
    -H "Content-Type: application/xml" \
    -H "X-Signature: sha256aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
    -d "$VALID_MESSAGE")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)
print_result "Invalid signature format (no equals)" "$HTTP_CODE" "401"
echo "Response: $BODY"
echo ""

# Test 7: Invalid algorithm in signature header
echo -e "${YELLOW}Test 7: Invalid algorithm (md5 instead of sha256)${NC}"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$ENDPOINT" \
    -H "Content-Type: application/xml" \
    -H "X-Signature: md5=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
    -d "$VALID_MESSAGE")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)
print_result "Invalid algorithm in signature" "$HTTP_CODE" "401"
echo "Response: $BODY"
echo ""

# Test 8: Empty body with valid signature
echo -e "${YELLOW}Test 8: Empty body with valid signature${NC}"
EMPTY_BODY=""
SIGNATURE=$(calculate_hmac "$EMPTY_BODY" "$HMAC_SECRET")
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$ENDPOINT" \
    -H "Content-Type: application/xml" \
    -H "X-Signature: sha256=$SIGNATURE" \
    -d "$EMPTY_BODY")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)
print_result "Empty body (should fail with 400, not 401)" "$HTTP_CODE" "400"
echo "Response: $BODY"
echo ""

# Summary
echo -e "${YELLOW}=== Test Summary ===${NC}"
echo "Tests completed. Review results above."
echo ""
echo "Expected patterns:"
echo "  • Valid HMAC → 200 OK"
echo "  • Missing header → 401 Unauthorized"
echo "  • Invalid signature → 401 Unauthorized"
echo "  • Wrong secret → 401 Unauthorized"
echo "  • Modified body → 401 Unauthorized"
echo "  • Invalid format → 401 Unauthorized"
echo "  • Wrong algorithm → 401 Unauthorized"
echo "  • Empty/invalid body → 400 Bad Request"
