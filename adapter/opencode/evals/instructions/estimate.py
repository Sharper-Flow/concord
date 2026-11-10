"""Offline nominal pilot estimates from calibration result and ledger records."""

import math
from collections import defaultdict

from .cases import CASES


def _number(value):
    return (not isinstance(value, bool) and isinstance(value, (int, float))
            and math.isfinite(value) and value >= 0)


def estimate(results: list[dict], *, sessions_per_model: int = 96, proposed_cap: float) -> dict:
    """Estimate one model's balanced pilot; results must include every claimed attempt.

    Accept result records from run.calibrate and ledger entries for attempts
    whose result is unavailable. The caller must reconcile the complete ledger;
    this function cannot discover omitted claims. Currency is unspecified unless
    the supplied usage records include a currency field.
    """
    if not _number(proposed_cap) or proposed_cap == 0:
        raise ValueError("proposed_cap must be finite and positive")
    if type(sessions_per_model) is not int or sessions_per_model <= 0 or sessions_per_model % len(CASES):
        raise ValueError("sessions_per_model must be a positive multiple of the case count")
    if not isinstance(results, list):
        raise ValueError("results must be a list of attempt records")

    families = {case["family"] for case in CASES.values()}
    samples = defaultdict(list)
    missing = []
    seen = set()
    subtotal = 0.0
    totals = defaultdict(float)
    sources, currencies = set(), set()
    observed_costs, observed_times = [], []
    for record in results:
        if not isinstance(record, dict):
            raise ValueError("attempt must be an object")
        attempt_id = record.get("attempt_id", record.get("id"))
        if not isinstance(attempt_id, str) or not attempt_id or attempt_id in seen:
            raise ValueError("attempt IDs must be present and unique")
        seen.add(attempt_id)
        case_id = record.get("case_id")
        if case_id not in CASES:
            raise ValueError("attempt must name a known case")
        usage = record.get("usage")
        nominal = usage.get("nominal_api_equivalent") if isinstance(usage, dict) else None
        accounted = _number(nominal)
        if accounted:
            subtotal += nominal
            observed_costs.append(nominal)
            raw_totals = usage.get("totals")
            if not isinstance(raw_totals, dict) or any(not _number(v) for v in raw_totals.values()):
                raise ValueError("accounted usage must have valid totals")
            for name, value in raw_totals.items():
                totals[name] += value
            source = usage.get("pricing_source")
            if not isinstance(source, str) or not source.strip():
                raise ValueError("accounted usage must include pricing_source")
            sources.add(source)
            currency = usage.get("currency")
            if currency is not None and (not isinstance(currency, str) or not currency.strip()):
                raise ValueError("currency must be a nonempty string")
            currencies.add(currency)
        elapsed = record.get("elapsed_seconds")
        if _number(elapsed):
            observed_times.append(elapsed)
        if record.get("status") != "completed" or not accounted:
            missing.append(attempt_id)
            continue
        if not _number(elapsed):
            missing.append(attempt_id)
            continue
        samples[case_id].append((nominal, elapsed))
    if len(currencies) > 1:
        raise ValueError("cannot combine different or unspecified currencies")

    family_samples = {family: [sample for case_id, group in samples.items()
                               if CASES[case_id]["family"] == family for sample in group]
                      for family in families}
    unsampled = sorted(family for family, group in family_samples.items() if not group)
    max_cost = max(observed_costs, default=None)
    max_time = max(observed_times, default=None)
    conservative = max_cost * sessions_per_model if max_cost is not None else None
    if conservative is not None and proposed_cap < conservative:
        raise ValueError("proposed_cap is below the max-observed extrapolation")

    per_case = {}
    count = sessions_per_model // len(CASES)
    for case_id, case in CASES.items():
        direct = samples[case_id]
        group = family_samples[case["family"]]
        costs = [v[0] for v in group]
        times = [v[1] for v in group]
        per_case[case_id] = {
            "family": case["family"], "sessions": count,
            "sample_basis": "family" if group else "unsampled_family_bound",
            "observed_samples": len(direct),
            "observed_cost_range": [min(v[0] for v in direct), max(v[0] for v in direct)] if direct else None,
            "observed_seconds_range": [min(v[1] for v in direct), max(v[1] for v in direct)] if direct else None,
            "expected_nominal": sum(costs) / len(costs) * count if costs else None,
            "expected_seconds": sum(times) / len(times) * count if times else None,
            "provisional_expected_nominal": (sum(costs) / len(costs) if costs else max_cost) * count if costs or max_cost is not None else None,
            "provisional_expected_seconds": (sum(times) / len(times) if times else max_time) * count if times or max_time is not None else None,
            "max_observed_nominal": max_cost * count if max_cost is not None else None,
            "max_observed_seconds": max_time * count if max_time is not None else None,
        }
    per_family = {}
    for family in sorted(families):
        group = family_samples[family]
        rows = [row for row in per_case.values() if row["family"] == family]
        per_family[family] = {
            "status": "sampled" if group else "inconclusive",
            "observed_samples": len(group),
            "observed_cost_range": [min(v[0] for v in group), max(v[0] for v in group)] if group else None,
            "observed_seconds_range": [min(v[1] for v in group), max(v[1] for v in group)] if group else None,
            "expected_nominal": sum(row["expected_nominal"] for row in rows) if group else None,
            "expected_seconds": sum(row["expected_seconds"] for row in rows) if group else None,
            "provisional_expected_nominal": sum(row["provisional_expected_nominal"] for row in rows) if all(row["provisional_expected_nominal"] is not None for row in rows) else None,
            "provisional_expected_seconds": sum(row["provisional_expected_seconds"] for row in rows) if all(row["provisional_expected_seconds"] is not None for row in rows) else None,
            "max_observed_nominal": sum(row["max_observed_nominal"] for row in rows) if max_cost is not None else None,
            "max_observed_seconds": sum(row["max_observed_seconds"] for row in rows) if max_time is not None else None,
        }
    complete = bool(results) and not missing and not unsampled
    return {
        "status": "complete" if complete else "incomplete",
        "missing_attempt_ids": missing, "unsampled_families": unsampled,
        "observed_known_nominal_subtotal": subtotal, "observed_known_totals": dict(totals),
        "currency": next(iter(currencies), None), "pricing_sources": sorted(sources),
        "sessions_per_model": sessions_per_model,
        "expected_nominal": sum(row["expected_nominal"] for row in per_case.values()) if complete else None,
        "provisional_expected_nominal": sum(row["provisional_expected_nominal"] for row in per_case.values()) if all(row["provisional_expected_nominal"] is not None for row in per_case.values()) else None,
        "max_observed_nominal": conservative,
        "expected_seconds": sum(row["expected_seconds"] for row in per_case.values()) if complete else None,
        "provisional_expected_seconds": sum(row["provisional_expected_seconds"] for row in per_case.values()) if all(row["provisional_expected_seconds"] is not None for row in per_case.values()) else None,
        "max_observed_seconds": max_time * sessions_per_model if max_time is not None else None,
        "cases": per_case, "families": per_family,
        "proposed_cap": proposed_cap,
        "cap_notice": "Administrative spending proposal, not mechanically enforced by this harness; provider retries are not bounded.",
        "limitations": "Nominal API equivalent only; not subscription billed spend. Provisional expectations use observed family means and unsampled_family_bound uses the observed maximum, not a guaranteed cap. Estimates assume all claimed attempts are supplied and sequential session time; observed maxima are not guaranteed bounds.",
        "regression_policy": "Any S3 or material family regression means undo the responsible merge. Calibration is not effectiveness evidence.",
    }
