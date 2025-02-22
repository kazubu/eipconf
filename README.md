# EIP Configuration Tool

This tool automates the configuration of GIF tunnels, VLANs, and bridges on a network interface based on a JSON configuration fetched from a specified URL. It supports dynamic updates, DNS resolution for destination hostnames, Slack notifications, and MTU settings for new interfaces.

## Features

- **GIF Tunnel Management**: Creates, updates, or removes GIF tunnels based on JSON config.
- **VLAN Configuration**: Manages VLAN interfaces tied to a physical interface.
- **Bridge Management**: Configures bridges with GIF and VLAN members.
- **DNS Resolution**: Resolves `dst_hostname` to IP addresses, prioritizing IPv6 if `src_addr` is IPv6.
- **Slack Notifications**: Sends `WARN` and `ERROR` logs, plus configuration diffs, to a Slack channel.
- **MTU Configuration**: Sets MTU to 1500 for newly created GIF tunnels and bridges.
- **Continuous Monitoring**: Periodically fetches and applies the configuration (default: every 30 seconds).

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

Create a `settings.json` file in the working directory with the following structure:

```json
{
    "url": "http://example.com/config.json",
    "physical_iface": "em2",
    "slack_webhook_url": "https://hooks.slack.com/services/xxx/yyy/zzz"
}
```

- `url`: URL to fetch the tunnel configuration JSON.
- `physical_iface`: Physical network interface (e.g., `em2`) for VLANs.
- `slack_webhook_url`: Optional Slack Webhook URL for notifications (can also be set via `SLACK_WEBHOOK_URL` environment variable).

### `config.json` (Remote)

The remote JSON configuration should follow this structure:

```json
[
    {
        "tunnel_id": "0",
        "src_addr": "2001:db8::1",
        "dst_addr": "2001:db8::2",
        "vlan_id": "100"
    },
    {
        "tunnel_id": "1",
        "src_addr": "192.168.1.1",
        "dst_hostname": "example.com",
        "vlan_id": "101"
    }
]
```

- `tunnel_id`: Unique identifier for the tunnel (e.g., `0`, `1`).
- `src_addr`: Source IP address (IPv4 or IPv6).
- `dst_addr`: Destination IP address (optional if `dst_hostname` is provided).
- `dst_hostname`: Destination hostname (resolved to IP, IPv6 prioritized if `src_addr` is IPv6).
- `vlan_id`: VLAN ID for the interface.

## Usage

1. Run the tool with root privileges:
   ```bash
   sudo ./eipconf
   ```

2. Optionally set the Slack Webhook URL via environment variable:
   ```bash
   export SLACK_WEBHOOK_URL="https://hooks.slack.com/services/xxx/yyy/zzz"
   sudo ./eipconf
   ```

The tool will:
- Fetch `config.json` from the specified URL every 30 seconds.
- Apply or update GIF tunnels, VLANs, and bridges as needed.
- Set MTU to 1500 for newly created GIF tunnels and bridges.
- Send Slack notifications for configuration diffs and errors (`WARN` or `ERROR`).

## Logging

- **INFO**: Configuration updates, command successes, and periodic checks (visible in console).
- **DEBUG**: Detailed operations (e.g., skipping unchanged interfaces, keeping existing `dst_addr`)—not shown by default.
- **WARN**: Non-critical issues (e.g., DNS resolution failures with fallback to existing `dst_addr`)—sent to Slack.
- **ERROR**: Critical failures (e.g., unresolvable hostnames for new tunnels, command failures)—sent to Slack.

To see `DEBUG` logs, modify the log level in the source code (`slog.HandlerOptions{Level: slog.LevelInfo}` to `Level: slog.LevelDebug`) and rebuild.

## Notes

- **MTU**: Only set to 1500 for newly created interfaces. Existing interfaces retain their current MTU.
- **DNS Resolution**: If `dst_hostname` fails to resolve or lacks a suitable IP (IPv6/IPv4 based on `src_addr`), existing tunnels keep their `dst_addr`, while new tunnels are skipped.
- **Slack**: Notifications include hostname, log level, and message details for `WARN`/`ERROR`, plus configuration diffs.

## Contributing

Feel free to submit issues or pull requests to enhance functionality or fix bugs.

## License

This project is licensed under MIT license.
