"""Strict accounting for recorded OpenCode assistant messages.

OpenCode records input without cache tokens and output without reasoning
tokens, and prices reasoning at the output rate; nominal pricing follows that
recording. Rates and their source are supplied by the caller, not inferred
from a model.
"""

import math


class AccountingError(ValueError):
    """A recording cannot support the requested accounting."""


def _number(value, label):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
        raise AccountingError(f"{label}: expected a finite nonnegative number")
    return value


def aggregate(messages, provider, model, rates, rate_source):
    """Account for every assistant message, including compaction summaries.

    Each entry is an API message envelope with an ``info`` object. Missing
    usage, recorded message errors and unexpected serving identities are
    errors, never zero estimates.
    """
    if not isinstance(rate_source, str) or not rate_source.strip():
        raise AccountingError("pricing source is required")
    rate_names = ("input", "output", "cache_read", "cache_write")
    try:
        prices = {name: _number(rates[name], f"rate {name}") for name in rate_names}
    except (KeyError, TypeError) as exc:
        raise AccountingError(f"missing pricing rate: {exc}") from exc
    totals = {name: 0 for name in ("input", "output", "reasoning", "cache_read", "cache_write", "host_cost")}
    count = 0
    for message in messages:
        info = message.get("info") if isinstance(message, dict) else None
        if not isinstance(info, dict):
            raise AccountingError("message envelope without an info object")
        if info.get("role") != "assistant":
            continue
        count += 1
        if (info.get("providerID"), info.get("modelID")) != (provider, model):
            raise AccountingError("serving provider/model missing or does not match configured identity")
        if info.get("error"):
            raise AccountingError("assistant message recorded an error; its usage may be incomplete")
        try:
            tokens = info["tokens"]
            values = {
                "input": tokens["input"], "output": tokens["output"],
                "reasoning": tokens["reasoning"], "cache_read": tokens["cache"]["read"],
                "cache_write": tokens["cache"]["write"], "host_cost": info["cost"],
            }
        except (KeyError, TypeError) as exc:
            raise AccountingError(f"missing assistant usage field: {exc}") from exc
        for name, value in values.items():
            totals[name] += _number(value, name)
    if not count:
        raise AccountingError("no assistant usage recorded")
    nominal = (totals["input"] * prices["input"]
               + (totals["output"] + totals["reasoning"]) * prices["output"]
               + totals["cache_read"] * prices["cache_read"]
               + totals["cache_write"] * prices["cache_write"]) / 1_000_000
    return {"assistant_messages": count, "totals": totals, "nominal_api_equivalent": nominal,
            "pricing_source": rate_source, "subscription_billed_amount": None,
            "pricing_assumption": "input excludes cache; output excludes reasoning; reasoning priced at the output rate"}
