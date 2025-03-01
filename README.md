# EIP Configuration Tool

The EIP Configuration Tool automates the configuration of GIF tunnels, VLANs, and bridge interfaces on a server by periodically fetching a JSON configuration from an HTTP endpoint or local file. In the latest version, an optional "description" field can be specified for each tunnel configuration. This description is applied to the corresponding GIF interface via ifconfig, and any changes (including those due to extra whitespace) are detected and updated accordingly.

## Features

- **GIF Tunnel Management**
  - Creates, updates, or removes GIF interfaces based on the JSON configuration.
  - Compares source and destination addresses, IPv6 settings, and the description field (after trimming whitespace) to detect differences.
- **VLAN Configuration**
  - Sets up VLAN interfaces on a specified physical interface and associates them with GIF tunnels.
- **Bridge Management**
  - Combines GIF and VLAN interfaces into bridges for integrated network connectivity.
- **Description Field**
  - An optional `description` can be included in each tunnel configuration.
  - The tool retrieves the current GIF interface’s description from ifconfig, trims whitespace, and compares it with the JSON value. If differences are detected, the GIF interface is updated.
- **DNS Resolution**
  - If a `dst_hostname` is provided, the tool resolves it to an IP address based on the specified or inferred IP version.
- **Default Source Address**
  - If `src_addr` is omitted, the tool uses a default source address or dynamically fetches the IP from a specified default interface.
- **Slack Notifications**
  - Warning and error logs—as well as configuration differences—can be sent to Slack. Slack channel, username, and icon can be configured.
- **MTU Setting**
  - New GIF tunnels and bridges are configured with an MTU of 1500.
- **Logging**
  - Detailed logging (DEBUG, INFO, WARN, ERROR) is output to the console and optionally to a log file.
- **Continuous Monitoring**
  - The tool fetches the JSON configuration at a configurable interval and applies updates only when differences are detected.

## Requirements

- Go 1.21 or later
- Root privileges (required for executing ifconfig commands)
- Slack Webhook URL (optional, for notifications)

## Installation

1. Clone the repository or download the source code:

``` bash
git clone <repository-url>
cd <repository-dir>
```

2. Build the binary:

``` bash
go build -o eipconf
```

## Configuration

### settings.json

Place a `settings.json` file in the same directory as the executable, or specify its path using the `--config`/`-c` flag or the `EIPCONF_CONF` environment variable. Below is an example configuration:

``` json
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

- **config_source**: URL or local file path for the tunnel configuration JSON (required).
- **physical_iface**: Physical network interface for VLANs (required).
- Other fields configure Slack notifications, logging, fetch interval, and default source address settings.

### config.json (Example)

The configuration file should be a JSON array as shown below:

``` json
[
    {
        "tunnel_id": "1",
        "src_addr": "2001:db8::1",
        "dst_addr": "2001:db8::2",
        "vlan_id": "100",
        "ip_version": "6",
        "description": "Production tunnel"
    },
    {
        "tunnel_id": "2",
        "dst_hostname": "example.com",
        "vlan_id": "101",
        "ip_version": "4",
        "description": "Backup tunnel"
    }
]
```

- **tunnel_id**: Unique identifier for the tunnel.
- **src_addr**: Source IP address (optional; if omitted, `default_src_addr` or `default_src_iface` is used).
- **dst_addr**: Destination IP address (or use `dst_hostname` for DNS resolution).
- **dst_hostname**: Destination hostname (DNS will be resolved).
- **vlan_id**: VLAN ID associated with the tunnel.
- **ip_version**: "4" for IPv4 or "6" for IPv6.
- **description**: Optional description applied to the GIF interface. Whitespace is trimmed before comparison, ensuring that any changes are detected and updated.

## Usage

Run the tool with root privileges:

```bash
sudo ./eipconf
```

To specify a custom settings file:

``` bash
sudo ./eipconf --config=/path/to/settings.json
```

For Slack notifications, you can also set the Slack Webhook URL via an environment variable:

``` bash
export SLACK_WEBHOOK_URL="https://hooks.slack.com/services/xxx/yyy/zzz"
sudo ./eipconf
```

The tool periodically fetches the JSON configuration and applies updates to the server’s interfaces when differences (including in the description field) are detected.

## Logging

- **DEBUG**: Detailed internal operations, including diff detection and ifconfig output parsing.
- **INFO**: Configuration updates, command execution results, and periodic check status.
- **WARN/ERROR**: Warnings and errors (also sent to Slack if configured).

Logs are output to both the console and to the file specified by `log_file`.

## Notes

- **Description Updates**
  The GIF interface’s description is updated if the JSON `description` differs from the current ifconfig output (after trimming extra whitespace). This ensures that even if the interface already exists, any changes in the description field are correctly detected and applied.

- **DNS Resolution**
  If `dst_hostname` is specified but cannot be resolved, the existing configuration is maintained or new tunnels are skipped.

- **Fetch Interval**
  The configuration is re-fetched every `fetch_interval` seconds, and updates are applied only when necessary.

## License

This project is licensed under the MIT License. See the LICENSE file for details.
