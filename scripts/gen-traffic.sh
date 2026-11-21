#!/usr/bin/env bash
# Sends a realistic mix of requests through the recording proxy.
set -euo pipefail
BASE=${1:-http://localhost:18080}
TOKEN="Bearer demo-token-123"
J='Content-Type: application/json'

curl -s "$BASE/products" > /dev/null
curl -s "$BASE/products?category=accessories" > /dev/null
curl -s "$BASE/products?category=audio" > /dev/null
curl -s "$BASE/products?category=home" > /dev/null
for id in 1 2 3 4 5; do curl -s "$BASE/products/$id" > /dev/null; done
curl -s "$BASE/products/999" > /dev/null          # 404
curl -s "$BASE/health" > /dev/null                # forwarded, not recorded

# stateful flow: create an order, then read it back
resp=$(curl -s -X POST "$BASE/orders" -H "$J" -d '{"items":[{"product_id":1,"qty":2},{"product_id":4,"qty":1}]}')
oid=$(printf '%s' "$resp" | sed -n 's/.*"id":"\([a-f0-9]*\)".*/\1/p')
curl -s "$BASE/orders/$oid" > /dev/null
curl -s -X POST "$BASE/orders" -H "$J" -d '{"items":[{"product_id":999,"qty":1}]}' > /dev/null   # 400
curl -s -X POST "$BASE/orders" -H "$J" -d '{"items":[]}' > /dev/null                               # 422
curl -s "$BASE/orders/doesnotexist" > /dev/null                                                    # 404

curl -s -H "Authorization: $TOKEN" "$BASE/users/me" > /dev/null
curl -s "$BASE/users/me" > /dev/null                                                               # 401
echo "traffic sent"
