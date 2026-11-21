import asyncio
import os
import uuid
from datetime import datetime, timezone

from fastapi import FastAPI, Header, HTTPException, Request
from pydantic import BaseModel, Field

# v1       baseline
# v2       good change: adds a "currency" field (additive, should pass the gate)
# v2-bad   breaking change: drops "stock", price becomes a string, unknown product returns 200
# v2-slow  same answers as v2, but the product list is slow
VERSION = os.getenv("APP_VERSION", "v1")

app = FastAPI(title="shop-api")

PRODUCTS = [
    {"id": 1, "name": "USB-C Cable", "category": "accessories", "price": 299.0, "stock": 120},
    {"id": 2, "name": "Phone Stand", "category": "accessories", "price": 449.0, "stock": 60},
    {"id": 3, "name": "Laptop Sleeve", "category": "accessories", "price": 899.0, "stock": 35},
    {"id": 4, "name": "Wireless Earbuds", "category": "audio", "price": 2499.0, "stock": 40},
    {"id": 5, "name": "Bluetooth Speaker", "category": "audio", "price": 3199.0, "stock": 18},
    {"id": 6, "name": "Desk Lamp", "category": "home", "price": 1299.0, "stock": 25},
    {"id": 7, "name": "Smart Plug", "category": "home", "price": 799.0, "stock": 80},
    {"id": 8, "name": "Water Bottle", "category": "home", "price": 549.0, "stock": 200},
]
ORDERS: dict = {}
DEMO_TOKEN = "Bearer demo-token-123"


class OrderItem(BaseModel):
    product_id: int
    qty: int = Field(ge=1, le=10)


class OrderIn(BaseModel):
    items: list[OrderItem] = Field(min_length=1)


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


def view(p: dict) -> dict:
    out = dict(p)
    if VERSION in ("v2", "v2-slow"):
        out["currency"] = "INR"
    if VERSION == "v2-bad":
        out.pop("stock")
        out["price"] = str(out["price"])
    return out


@app.middleware("http")
async def add_request_id(request: Request, call_next):
    response = await call_next(request)
    response.headers["X-Request-Id"] = uuid.uuid4().hex
    return response


@app.get("/health")
def health():
    return {"status": "ok", "version": VERSION}


@app.get("/products")
async def list_products(category: str | None = None):
    if VERSION == "v2-slow":
        await asyncio.sleep(0.25)
    items = [p for p in PRODUCTS if category is None or p["category"] == category]
    return {"count": len(items), "items": [view(p) for p in items]}


@app.get("/products/{product_id}")
def get_product(product_id: int):
    for p in PRODUCTS:
        if p["id"] == product_id:
            return view(p)
    if VERSION == "v2-bad":
        return {"detail": None}
    raise HTTPException(status_code=404, detail="product not found")


@app.post("/orders", status_code=201)
def create_order(order: OrderIn):
    lines, total = [], 0.0
    for it in order.items:
        p = next((x for x in PRODUCTS if x["id"] == it.product_id), None)
        if p is None:
            raise HTTPException(status_code=400, detail=f"unknown product {it.product_id}")
        line_total = p["price"] * it.qty
        lines.append({"product_id": p["id"], "qty": it.qty, "line_total": line_total})
        total += line_total
    oid = uuid.uuid4().hex[:12]
    ORDERS[oid] = {"id": oid, "status": "created", "items": lines,
                   "total": round(total, 2), "created_at": now()}
    return ORDERS[oid]


@app.get("/orders/{order_id}")
def get_order(order_id: str):
    if order_id not in ORDERS:
        raise HTTPException(status_code=404, detail="order not found")
    return ORDERS[order_id]


@app.get("/users/me")
def me(authorization: str | None = Header(default=None)):
    if authorization != DEMO_TOKEN:
        raise HTTPException(status_code=401, detail="invalid token")
    return {"id": 1, "name": "Asha Verma", "email": "asha.verma@example.com",
            "phone": "+91-9000000000", "last_login": now()}
