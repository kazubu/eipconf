# EIP Configuration Tool

This tool automates the configuration of GIF tunnels, VLANs, and bridges on a network interface based on a JSON configuration fetched from a specified source (URL or local file). It supports dynamic updates, DNS resolution for destination hostnames, customizable Slack notifications, and MTU settings for new interfaces.

## Features

- **GIF Tunnel Management**: Creates, updates, or removes GIF tunnels based on JSON config.
- **VLAN Configuration**: Manages VLAN interfaces tied to a physical interface.
- **Bridge Management**: Configures bridges with GIF and VLAN members.
- **IP Version Specification**: Explicitly specify IPv4 or IPv6 for tunnels via `ip_version` ("4" or "6").
- **DNS Resolution**: Resolves `dst_hostname` to IP addresses, respecting the specified or inferred IP version (IPv6 prioritized if `src_addr` is IPv6).
- **Default Source Configuration**: Optionally set a default source address (`default_src_addr`) or interface (`default_src_iface`) in `settings.json`, allowing `src_addr` omission in the config source; dynamically tracks interface address changes if `default_src_iface` is used.
- **Slack Notifications**: Sends `WARN` and `ERROR` logs, plus configuration diffs, to a Slack channel with customizable channel, username, and icon; color-coded for clarity (green for diffs, yellow for WARN, red for ERROR).
- **MTU Configuration**: Sets MTU to 1500 for newly created GIF tunnels and bridges.
- **Logging**: Configurable log levels (DEBUG, INFO, WARN, ERROR) and optional log file output.
- **Continuous Monitoring**: Periodically fetches and applies the configuration with a configurable interval (default: 30 seconds).

## Prerequisites

- Go 1.21 or later
- Root privileges (required for `ifconfig` commands)
- A Slack Webhook URL (optional, for notifications)

## Installation

1. Clone the repository or copy the source code:
   ```bash
   git clone <repository-url>
   cd <repository-dir>
   ```

2. Build the binary:
   ```bash
   go build -o eipconf
   ```

## Configuration

### `settings.json`

Create a `settings.json` file in the same directory as the executable (or specify via `--config`/`-c` flag or `EIPCONF_CONF` environment variable) with the following structure:

```json
{
    "config_source": "http://example.com/config.json",
    "physical_iface": "em2",
    "slack_webhook_url": "https://hooks.slack.com/services/xxx/yyy/zzz",
    "slack_channel": "#network-updates",
    "slack_username": "EIPBot",
    "slack_icon_emoji": ":gear:",
    "log_level": "DEBUG",
    "log_file": "/var/log/eipconf.log",
    "fetch_interval": 60,
    "default_src_addr": "2001:db8::1",
    "default_src_iface": "em0"
}
```

- `config_source`: URL or local file path to fetch the tunnel configuration JSON (required).
- `physical_iface`: Physical network interface (e.g., `em2`) for VLANs (required).
- `slack_webhook_url`: Slack Webhook URL for notifications (optional, can be set via `SLACK_WEBHOOK_URL` environment variable).
- `slack_channel`: Slack channel name (e.g., `#network-updates`, optional).
- `slack_username`: Slack username (e.g., `EIPBot`, optional).
- `slack_icon_emoji`: Slack icon emoji (e.g., `:gear:`, optional).
- `log_level`: Log level (`DEBUG`, `INFO`, `WARN`, `ERROR`; optional, defaults to `INFO`).
- `log_file`: Path to log file (e.g., `/var/log/eipconf.log`; optional, logs to console if unspecified).
- `fetch_interval`: Interval in seconds to fetch the config (optional, defaults to 30).
- `default_src_addr`: Default source IP address (e.g., `2001:db8::1`, optional; used if `src_addr` is omitted in config).
- `default_src_iface`: Default source interface (e.g., `em0`, optional; dynamically fetches address if `src_addr` is omitted).

### `config.json` (Config Source)

The configuration source (URL or local file) should follow this structure:

```json
[
    {
        "tunnel_id": "0",
        "dst_addr": "2001:db8::2",
        "vlan_id": "100",
        "ip_version": "6"
    },
    {
        "tunnel_id": "1",
        "dst_hostname": "example.com",
        "vlan_id": "101",
        "ip_version": "4"
    }
]
```

- `tunnel_id`: Unique identifier for the tunnel (e.g., `0`, `1`).
- `src_addr`: Source IP address (IPv4 or IPv6, optional if `default_src_addr` or `default_src_iface` is set in `settings.json`).
- `dst_addr`: Destination IP address (optional if `dst_hostname` is provided).
- `dst_hostname`: Destination hostname (resolved to IP, respecting `ip_version` or inferred from `src_addr`).
- `vlan_id`: VLAN ID for the interface.
- `ip_version`: IP version (`"4"` for IPv4, `"6"` for IPv6; optional, inferred from `src_addr` if omitted).

## Usage

1. Run the tool with root privileges (default settings file in executable directory):
   ```bash
   sudo ./eipconf
   ```

2. Specify a custom settings file via command-line flag:
   ```bash
   sudo ./eipconf --config=/path/to/custom.json
   # or
   sudo ./eipconf -c=/path/to/custom.json
   ```

3. Specify via environment variable:
   ```bash
   export EIPCONF_CONF=/path/to/env.json
   sudo ./eipconf
   ```

4. Optionally set the Slack Webhook URL via environment variable:
   ```bash
   export SLACK_WEBHOOK_URL="https://hooks.slack.com/services/xxx/yyy/zzz"
   sudo ./eipconf
   ```

The tool will:
- Fetch `config.json` from the specified `config_source` at the configured interval (e.g., every 60 seconds if set).
- Apply or update GIF tunnels, VLANs, and bridges as needed, using `default_src_addr` or `default_src_iface` if `src_addr` is omitted.
- Set MTU to 1500 for newly created GIF tunnels and bridges.
- Output logs to console and/or a file based on `log_level` and `log_file`.
- Send Slack notifications for configuration diffs (green) and `WARN` (yellow) or `ERROR` (red) logs.

## Logging

- **DEBUG**: Detailed operations (e.g., skipping unchanged interfaces, keeping existing `dst_addr`).
- **INFO**: Configuration updates, command successes, and periodic checks (not sent to Slack).
- **WARN**: Non-critical issues (e.g., DNS resolution failures with fallback to existing `dst_addr`)—sent to Slack in yellow.
- **ERROR**: Critical failures (e.g., unresolvable hostnames for new tunnels, command failures)—sent to Slack in red.

- **Log Level**: Set via `log_level` in `settings.json` (default: `INFO`).
- **Log File**: If `log_file` is specified, logs at or above the configured level are written to the file in addition to the console.

## Notes

- **MTU**: Only set to 1500 for newly created interfaces. Existing interfaces retain their current MTU.
- **DNS Resolution**: If `dst_hostname` fails to resolve or lacks a suitable IP (based on `ip_version` or inferred from `src_addr`), existing tunnels keep their `dst_addr`, while new tunnels are skipped.
- **Default Source**:
  - If `src_addr` is omitted, `default_src_addr` or `default_src_iface` from `settings.json` is used.
  - `default_src_iface` dynamically fetches the interface's address each time the config is fetched, adapting to IP changes.
- **Slack**: Notifications include configuration diffs (green) and `WARN`/`ERROR` logs (yellow/red). `slack_channel`, `slack_username`, and `slack_icon_emoji` are optional; if unset, they are omitted from the payload, using the Webhook's defaults.
- **Fetch Interval**: Configurable via `fetch_interval` in seconds (default: 30). Controls how often the config is fetched.
- **Settings File Location**: By default, `settings.json` is expected in the executable directory, but can be overridden with `--config`/`-c` or `EIPCONF_CONF`.

## Contributing

Feel free to submit issues or pull requests to enhance functionality or fix bugs.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
