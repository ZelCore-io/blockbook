#!/usr/bin/env python3
"""Read-only GRS replica comparison. Never submits transactions or calls node RPC."""
import json
import subprocess
from datetime import datetime, timezone

BASES = ["https://blockbook.grs-2.zelcore.io", "https://blockbook.grs-1.zelcore.io"]


def get(base, path):
    response = subprocess.run(["curl", "--fail", "--silent", "--show-error", "--max-time", "30", base + path], check=True, capture_output=True, text=True)
    return json.loads(response.stdout)


def compare(path, keys=None):
    values = [get(base, path) for base in BASES]
    selected = [{key: value[key] for key in keys} for value in values] if keys else values
    if selected[0] != selected[1]:
        raise AssertionError(f"replicas differ for {path}: {selected}")
    return values[0]


if __name__ == "__main__":
    status = [get(base, "/api/") for base in BASES]
    for value in status:
        assert value["blockbook"]["inSync"] and not value["blockbook"]["initialSync"]
        assert value["blockbook"]["inSyncMempool"]
    assert status[0]["blockbook"]["gitCommit"] == "c5ade771de895035480adf0f411e6250ff243183"
    assert all(value["blockbook"]["coin"] == "Groestlcoin" for value in status)
    height = min(value["blockbook"]["bestHeight"] for value in status) - 10
    block = compare(f"/api/v2/block/{height}", ["hash", "height", "txCount"])
    txid = block["txs"][0]["txid"]
    tx = compare(f"/api/v2/tx/{txid}", ["txid", "blockHash", "blockHeight", "vin", "vout", "value"])
    address = next(output["addresses"][0] for output in tx["vout"] if output.get("isAddress") and output.get("addresses"))
    compare(f"/api/v2/address/{address}?details=basic", ["address", "balance", "totalReceived", "totalSent", "txs"])
    utxos = compare(f"/api/v2/utxo/{address}?confirmed=true")
    print(json.dumps({"checkedAt": datetime.now(timezone.utc).isoformat(), "bases": BASES,
        "status": status, "sample": {"height": height, "blockHash": block["hash"], "txid": txid, "address": address, "confirmedUtxoCount": len(utxos)},
        "checks": ["synced status", "same confirmed block", "same transaction inputs/outputs and values", "same address totals", "same confirmed UTXOs"]}, indent=2))
