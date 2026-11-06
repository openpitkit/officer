<!-- release-version: 0.3.0 -->
Adds live trading through IB, Binance and eToro connections, with guided live-account steps in the trading connection setup. Spot Funds gains a limit mode for trading from zero and unrealized and total P&L bounds. The web framework now exports its login, page frame, error banner, host router and styles, so a host application can build its own panel on top of it.

- New IB, Binance and eToro trading connections, with live trading on every venue
- New Spot Funds limit mode and unrealized and total P&L bounds
- Reusable web framework building blocks for host applications
- Officer listing in the MCP Registry, with the release version reported by the MCP server
- Hardened storage: owner-only SQLite database files and a release database protected from non-release builds

### Breaking changes

- Market-data instance responses no longer carry credentials - do not read them back from the API
