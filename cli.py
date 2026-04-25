#!/usr/bin/env python3
"""
CLI entry point for Kraken Trading Bot
"""
import argparse
import sys
from pathlib import Path

# Add backend to path
backend_path = Path(__file__).parent / "backend"
sys.path.insert(0, str(backend_path))

from config.settings import settings
from observability.logging import get_logger, setup_logging

logger = get_logger("cli")

def main():
    """Main CLI entry point"""
    parser = argparse.ArgumentParser(description="Kraken Trading Bot CLI")
    parser.add_argument(
        "--config",
        type=str,
        help="Path to config file"
    )
    parser.add_argument(
        "--mode",
        choices=["manual", "semi_automated", "fully_automated"],
        help="Override trading mode"
    )
    parser.add_argument(
        "--environment",
        choices=["paper", "live"],
        help="Override trading environment"
    )

    subparsers = parser.add_subparsers(dest="command", help="Available commands")

    # Status command
    subparsers.add_parser("status", help="Show bot status")

    # Approve command
    approve_parser = subparsers.add_parser("approve", help="Approve pending trade")
    approve_parser.add_argument("trade_id", help="Trade ID to approve")

    # Reject command
    reject_parser = subparsers.add_parser("reject", help="Reject pending trade")
    reject_parser.add_argument("trade_id", help="Trade ID to reject")

    # Run command
    subparsers.add_parser("run", help="Run the trading bot")

    args = parser.parse_args()

    # Setup logging
    setup_logging(settings.log_level, settings.log_file)

    logger.info("CLI started", extra={"command": args.command, "cli_args": vars(args)})

    if args.command == "status":
        show_status()
    elif args.command == "approve":
        approve_trade(args.trade_id)
    elif args.command == "reject":
        reject_trade(args.trade_id)
    elif args.command == "run":
        run_bot()
    else:
        parser.print_help()

def show_status():
    """Show current bot status"""
    print("Trading Bot Status")
    print(f"Mode: {settings.trading_mode}")
    print(f"Environment: {settings.trading_environment}")
    print(f"Starting Capital: £{settings.starting_capital:.2f}")
    print(f"Max Loss Per Trade: {settings.max_loss_per_trade_percent:.1f}%")
    print(f"Max Daily Loss: {settings.max_daily_loss_percent:.1f}%")
    print(f"Database: {settings.database_url}")
    print(f"Log Level: {settings.log_level}")

def _api_post(path: str) -> dict:
    """POST to the running bot API and return parsed JSON."""
    import json
    import urllib.error
    import urllib.request
    url = f"http://{settings.host}:{settings.port}{path}"
    req = urllib.request.Request(url, method="POST", data=b"",
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        print(f"API error {e.code}: {body}", file=sys.stderr)
        sys.exit(1)
    except urllib.error.URLError as e:
        print(f"Cannot reach bot at {url}: {e.reason}", file=sys.stderr)
        print("Is the bot running? Start it with:  python backend/main.py", file=sys.stderr)
        sys.exit(1)


def approve_trade(trade_id: str):
    """Approve a pending trade via the running bot API."""
    result = _api_post(f"/api/approvals/{trade_id}/approve")
    print(f"Approved: {result}")
    logger.info("Trade approved via CLI", extra={"trade_id": trade_id, "result": result})


def reject_trade(trade_id: str):
    """Reject a pending trade via the running bot API."""
    result = _api_post(f"/api/approvals/{trade_id}/reject")
    print(f"Rejected: {result}")
    logger.info("Trade rejected via CLI", extra={"trade_id": trade_id, "result": result})


def run_bot():
    """Start the trading bot (FastAPI + uvicorn)."""
    import subprocess
    backend = Path(__file__).parent / "backend"
    print(f"Starting bot on http://{settings.host}:{settings.port} ...")
    logger.info("Bot started via CLI")
    subprocess.run(
        [sys.executable, str(backend / "main.py")],
        check=True,
    )

if __name__ == "__main__":
    main()