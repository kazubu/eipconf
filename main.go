package main

import (
    "bytes"
    "context"
    "encoding/json"
    "flag"
    "fmt"
    "path/filepath"
    "io/ioutil"
    "log/slog"
    "net"
    "net/http"
    "os"
    "os/exec"
    "os/signal"
    "regexp"
    "strings"
    "syscall"
    "time"
)

type Settings struct {
    ConfigSource         string `json:"config_source"`
    PhysicalIface        string `json:"physical_iface"`
    SlackWebhookURL      string `json:"slack_webhook_url,omitempty"`
    SlackChannel         string `json:"slack_channel,omitempty"`
    SlackUsername        string `json:"slack_username,omitempty"`
    SlackIconEmoji       string `json:"slack_icon_emoji,omitempty"`
    LogLevel             string `json:"log_level,omitempty"`
    LogFile              string `json:"log_file,omitempty"`
    FetchInterval        int    `json:"fetch_interval,omitempty"`
    DefaultSrcAddr       string `json:"default_src_addr,omitempty"`
    DefaultSrcIface      string `json:"default_src_iface,omitempty"`
}


type TunnelConfig struct {
    TunnelID    string `json:"tunnel_id"`
    SrcAddr     string `json:"src_addr,omitempty"`
    DstAddr     string `json:"dst_addr"`
    DstHostname string `json:"dst_hostname,omitempty"`
    VlanID      string `json:"vlan_id"`
    IPVersion   string `json:"ip_version,omitempty"` // "4" または "6"
    Description string `json:"description,omitempty"`
}

type InterfaceConfig struct {
    Src      string
    Dst      string
    Vlan     string
    IsIPv6   bool
    TunnelID string
    Description string
}

type BridgeConfig struct {
    Members  []string
    TunnelID string
}

type SlackHandler struct {
    slog.Handler
    Settings *Settings
}

func (h *SlackHandler) Handle(ctx context.Context, r slog.Record) error {
    err := h.Handler.Handle(ctx, r)
    if r.Level >= slog.LevelWarn && h.Settings.SlackWebhookURL != "" {
        hostname, err := os.Hostname()
        if err != nil {
            hostname = "unknown"
        }

        var levelStr string
        switch r.Level {
        case slog.LevelWarn:
            levelStr = "[WARN]"
        case slog.LevelError:
            levelStr = "[ERROR]"
        default:
            levelStr = "[unknown]"
        }

        msg := fmt.Sprintf("[%s] %s: %s", hostname, levelStr, r.Message)
        var attrs []slog.Attr
        r.Attrs(func(a slog.Attr) bool {
            attrs = append(attrs, a)
            return true
        })
        for _, attr := range attrs {
            valueStr := fmt.Sprintf("%v", attr.Value)
            msg += fmt.Sprintf(" %s=`%s`", attr.Key, valueStr)
        }

        go sendToSlack(msg, r.Level, h.Settings)
    }
    return err
}

func sendToSlack(message string, level slog.Level, settings *Settings) {
    var color string
    switch level {
    case slog.LevelWarn:
        color = "#FF9900" // 黄色
    case slog.LevelError:
        color = "#FF0000" // 赤色
    default:
        color = "#36A64F" // 緑色（差分通知など）
    }

    attachment := map[string]interface{}{
        "color": color,
        "text":  message,
    }

    payload := make(map[string]interface{})
    payload["attachments"] = []interface{}{attachment}

    if settings.SlackChannel != "" {
        payload["channel"] = settings.SlackChannel
    }
    if settings.SlackUsername != "" {
        payload["username"] = settings.SlackUsername
    }
    if settings.SlackIconEmoji != "" {
        payload["icon_emoji"] = settings.SlackIconEmoji
    }

    payloadBytes, _ := json.Marshal(payload)

    resp, err := http.Post(settings.SlackWebhookURL, "application/json", bytes.NewBuffer(payloadBytes))
    if err != nil {
        fmt.Fprintf(os.Stderr, "Failed to send to Slack: %v\n", err)
        return
    }
    defer resp.Body.Close()
}


// notifyConfigDiff は差分をslog経由でINFOとして出力し、Slackにも通知
func notifyConfigDiff(gifsToAdd, gifsToModify, gifsToRemove map[string]InterfaceConfig, bridgesToAdd, bridgesToRemove map[string]BridgeConfig, settings *Settings) {
    if len(gifsToAdd) == 0 && len(gifsToModify) == 0 && len(gifsToRemove) == 0 && len(bridgesToAdd) == 0 && len(bridgesToRemove) == 0 {
        return
    }

    hostname, err := os.Hostname()
    if err != nil {
        hostname = "unknown"
    }

    var msg strings.Builder
    msg.WriteString(fmt.Sprintf("Configuration updated on %s:\n", hostname))

    if len(gifsToAdd) > 0 {
        msg.WriteString("Added tunnels:\n")
        for _, config := range gifsToAdd {
            // すべての動的値をバックティックで囲む
            msg.WriteString(fmt.Sprintf("- tunnel_id=`%s`, src_addr=`%s`, dst_addr=`%s`, vlan_id=`%s`, description=`%s`\n", config.TunnelID, config.Src, config.Dst, config.Vlan, config.Description))
        }
    }

    if len(gifsToModify) > 0 {
        msg.WriteString("Modified tunnels:\n")
        for _, config := range gifsToModify {
            msg.WriteString(fmt.Sprintf("- tunnel_id=`%s`, src_addr=`%s`, dst_addr=`%s`, vlan_id=`%s`, description=`%s`\n", config.TunnelID, config.Src, config.Dst, config.Vlan, config.Description))
        }
    }

    if len(gifsToRemove) > 0 {
        msg.WriteString("Removed tunnels:\n")
        for _, config := range gifsToRemove {
            msg.WriteString(fmt.Sprintf("- tunnel_id=`%s`, src_addr=`%s`, dst_addr=`%s`, vlan_id=`%s`\n", config.TunnelID, config.Src, config.Dst, config.Vlan))
        }
    }

    slog.Info(msg.String())

    if settings.SlackWebhookURL != "" {
        go sendToSlack(msg.String(), slog.LevelInfo, settings)
    }
}

// runCommand はコマンドを実行し、エラーがあれば再試行する
func runCommand(cmd string, args ...string) error {
    for attempt := 0; attempt < 3; attempt++ {
        command := exec.Command(cmd, args...)
        output, err := command.CombinedOutput()
        if err == nil {
            slog.Info("Command succeeded", "cmd", cmd, "args", args)
            return nil
        }
        outputStr := string(output)
        if strings.Contains(outputStr, "already exists") {
            slog.Info("Interface already exists, skipping creation", "cmd", cmd, "args", args)
            return nil
        }
        slog.Error("Command failed", "cmd", cmd, "args", args, "error", outputStr)
        if attempt < 2 {
            slog.Info("Retrying in 1 second")
            time.Sleep(time.Second)
        }
    }
    return fmt.Errorf("command failed after 3 attempts: %s %v", cmd, args)
}

// getCurrentInterfaces は現在のgif、VLAN、bridgeインターフェースを取得
func getCurrentInterfaces() (map[string]InterfaceConfig, map[string]BridgeConfig, map[string]string) {
    gifInterfaces := make(map[string]InterfaceConfig)
    bridgeInterfaces := make(map[string]BridgeConfig)
    vlanInterfaces := make(map[string]string)

    output, err := exec.Command("ifconfig", "-a").Output()
    if err != nil {
        slog.Error("Failed to get current interfaces", "error", err)
        return gifInterfaces, bridgeInterfaces, vlanInterfaces
    }

    lines := strings.Split(string(output), "\n")
    for _, line := range lines {
        if strings.HasPrefix(line, "gif") {
            gifName := regexp.MustCompile(`gif\d+`).FindString(line)
            if gifName != "" {
                detail, _ := exec.Command("ifconfig", gifName).Output()
                detailStr := string(detail)
                var desc string
                for _, l := range strings.Split(detailStr, "\n") {
                    l = strings.TrimSpace(l)
                    if strings.HasPrefix(l, "description:") {
                        desc = strings.TrimSpace(strings.TrimPrefix(l, "description:"))
                        break
                    }
                }
                srcDstIPv4 := regexp.MustCompile(`tunnel inet (\S+) --> (\S+)`).FindStringSubmatch(detailStr)
                srcDstIPv6 := regexp.MustCompile(`tunnel inet6 (\S+) --> (\S+)`).FindStringSubmatch(detailStr)
                tunnelID := strings.TrimPrefix(gifName, "gif")
                if len(srcDstIPv4) == 3 {
                    gifInterfaces[gifName] = InterfaceConfig{Src: srcDstIPv4[1], Dst: srcDstIPv4[2], Vlan: "", IsIPv6: false, TunnelID: tunnelID, Description: desc}
                } else if len(srcDstIPv6) == 3 {
                    gifInterfaces[gifName] = InterfaceConfig{Src: srcDstIPv6[1], Dst: srcDstIPv6[2], Vlan: "", IsIPv6: true, TunnelID: tunnelID, Description: desc}
                } else {
                    gifInterfaces[gifName] = InterfaceConfig{Src: "", Dst: "", Vlan: "", IsIPv6: false, TunnelID: tunnelID, Description: desc}
                }
            }
        }
        if strings.HasPrefix(line, "bridge") {
            bridgeName := regexp.MustCompile(`bridge\d+`).FindString(line)
            if bridgeName != "" {
                detail, _ := exec.Command("ifconfig", bridgeName).Output()
                members := regexp.MustCompile(`member: (\S+)`).FindAllStringSubmatch(string(detail), -1)
                memberList := []string{}
                for _, m := range members {
                    memberList = append(memberList, m[1])
                }
                tunnelID := strings.TrimPrefix(bridgeName, "bridge")
                bridgeInterfaces[bridgeName] = BridgeConfig{Members: memberList, TunnelID: tunnelID}
            }
        }
        if regexp.MustCompile(`\w+\.\d+`).MatchString(line) {
            vlanName := regexp.MustCompile(`\w+\.\d+`).FindString(line)
            if vlanName != "" {
                detail, _ := exec.Command("ifconfig", vlanName).Output()
                vlanID := regexp.MustCompile(`vlan: (\d+)`).FindStringSubmatch(string(detail))
                if len(vlanID) == 2 {
                    vlanInterfaces[vlanName] = vlanID[1]
                }
            }
        }
    }
    return gifInterfaces, bridgeInterfaces, vlanInterfaces
}

func loadSettings(filename string) (Settings, error) {
    data, err := ioutil.ReadFile(filename)
    if err != nil {
        return Settings{}, fmt.Errorf("failed to read settings file: %v", err)
    }

    var settings Settings
    if err := json.Unmarshal(data, &settings); err != nil {
        return Settings{}, fmt.Errorf("failed to unmarshal settings: %v", err)
    }

    if settings.ConfigSource == "" || settings.PhysicalIface == "" {
        return Settings{}, fmt.Errorf("ConfigSource or PhysicalIface is not specified in settings file")
    }

    if envURL := os.Getenv("SLACK_WEBHOOK_URL"); envURL != "" {
        settings.SlackWebhookURL = envURL
    }

    if settings.FetchInterval <= 0 {
        settings.FetchInterval = 30
    }

    return settings, nil
}

// getInterfaceAddr は指定されたインターフェイスのアドレスを取得
func getInterfaceAddr(iface string, isIPv6 bool) (string, error) {
    output, err := exec.Command("ifconfig", iface).Output()
    if err != nil {
        return "", fmt.Errorf("failed to get interface address: %v", err)
    }

    lines := strings.Split(string(output), "\n")
    for _, line := range lines {
        if strings.Contains(line, "inet") {
            if isIPv6 && strings.Contains(line, "inet6") {
                fields := strings.Fields(line)
                for _, field := range fields {
                    if net.ParseIP(field) != nil && strings.Contains(field, ":") {
                        return field, nil
                    }
                }
            } else if !isIPv6 && strings.Contains(line, "inet") && !strings.Contains(line, "inet6") {
                fields := strings.Fields(line)
                for _, field := range fields {
                    if net.ParseIP(field) != nil && !strings.Contains(field, ":") {
                        return field, nil
                    }
                }
            }
        }
    }
    return "", fmt.Errorf("no suitable address found for interface %s (IPv6: %v)", iface, isIPv6)
}

// fetchConfig は指定されたソース（URLまたはローカルファイル）からトンネル設定を取得し、重複と欠落をチェック
func fetchConfig(source string, currentGifs map[string]InterfaceConfig, settings Settings) ([]TunnelConfig, error) {
    var body []byte
    var err error

    if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
        resp, err := http.Get(source)
        if err != nil {
            return nil, fmt.Errorf("failed to fetch config from URL: %v", err)
        }
        defer resp.Body.Close()

        body, err = ioutil.ReadAll(resp.Body)
        if err != nil {
            return nil, fmt.Errorf("failed to read response body: %v", err)
        }
    } else {
        body, err = ioutil.ReadFile(source)
        if err != nil {
            return nil, fmt.Errorf("failed to read config file: %v", err)
        }
    }

    var configs []TunnelConfig
    if err := json.Unmarshal(body, &configs); err != nil {
        return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
    }

    var validConfigs []TunnelConfig
    tunnelIDs := make(map[string]bool)
    dstAddrs := make(map[string]bool)
    vlanIDs := make(map[string]bool)

    for i, config := range configs {
        if config.TunnelID == "" {
            slog.Error("Skipping tunnel due to missing field", "index", i, "reason", "missing tunnel_id")
            continue
        }
        if config.VlanID == "" {
            slog.Error("Skipping tunnel due to missing field", "index", i, "reason", "missing vlan_id")
            continue
        }

        // IPバージョンの設定
        isIPv6 := false
        switch config.IPVersion {
        case "6":
            isIPv6 = true
        case "4":
            isIPv6 = false
        case "":
            // IPバージョン未指定の場合、デフォルトでソースアドレスから推測
            if config.SrcAddr != "" {
                isIPv6 = strings.Contains(config.SrcAddr, ":")
            }
        default:
            slog.Error("Skipping tunnel due to invalid ip_version", "index", i, "tunnel_id", config.TunnelID, "ip_version", config.IPVersion)
            continue
        }

        // ソースアドレスの設定
        if config.SrcAddr == "" {
            if settings.DefaultSrcIface != "" {
                addr, err := getInterfaceAddr(settings.DefaultSrcIface, isIPv6)
                if err != nil {
                    slog.Error("Failed to get interface address", "interface", settings.DefaultSrcIface, "isIPv6", isIPv6, "error", err)
                    continue
                }
                config.SrcAddr = addr
                slog.Info("Using interface address as src_addr", "tunnel_id", config.TunnelID, "interface", settings.DefaultSrcIface, "src_addr", config.SrcAddr)
            } else if settings.DefaultSrcAddr != "" {
                config.SrcAddr = settings.DefaultSrcAddr
                slog.Info("Using default src_addr", "tunnel_id", config.TunnelID, "src_addr", config.SrcAddr)
            } else {
                slog.Error("Skipping tunnel due to missing src_addr and no default specified", "index", i, "tunnel_id", config.TunnelID)
                continue
            }
        }

        gif := fmt.Sprintf("gif%s", config.TunnelID)
        if config.DstAddr == "" && config.DstHostname != "" {
            ips, err := net.LookupIP(config.DstHostname)
            if err != nil {
                if current, exists := currentGifs[gif]; exists {
                    config.DstAddr = current.Dst
                    slog.Warn("Failed to resolve dst_hostname, using existing dst_addr", "tunnel_id", config.TunnelID, "dst_hostname", config.DstHostname, "dst_addr", config.DstAddr, "error", err)
                } else {
                    slog.Error("Skipping tunnel due to unresolvable dst_hostname", "index", i, "tunnel_id", config.TunnelID, "dst_hostname", config.DstHostname, "error", err)
                    continue
                }
            } else {
                var resolvedAddr string
                if current, exists := currentGifs[gif]; exists {
                    for _, ip := range ips {
                        if ip.String() == current.Dst {
                            resolvedAddr = current.Dst
                            slog.Debug("Keeping existing dst_addr from resolved IPs", "tunnel_id", config.TunnelID, "dst_hostname", config.DstHostname, "dst_addr", resolvedAddr)
                            break
                        }
                    }
                }
                if resolvedAddr == "" {
                    for _, ip := range ips {
                        if isIPv6 && ip.To4() == nil {
                            resolvedAddr = ip.String()
                            break
                        } else if !isIPv6 && ip.To4() != nil {
                            resolvedAddr = ip.String()
                            break
                        }
                    }
                    if resolvedAddr == "" {
                        if current, exists := currentGifs[gif]; exists {
                            resolvedAddr = current.Dst
                            slog.Warn("No suitable IP found for dst_hostname, using existing dst_addr", "tunnel_id", config.TunnelID, "dst_hostname", config.DstHostname, "dst_addr", resolvedAddr, "isIPv6", isIPv6)
                        } else {
                            slog.Error("Skipping tunnel due to no suitable IP for dst_hostname", "index", i, "tunnel_id", config.TunnelID, "dst_hostname", config.DstHostname, "isIPv6", isIPv6)
                            continue
                        }
                    } else {
                        slog.Info("Resolved dst_hostname to IP", "tunnel_id", config.TunnelID, "dst_hostname", config.DstHostname, "dst_addr", resolvedAddr)
                    }
                }
                config.DstAddr = resolvedAddr
            }
        } else if config.DstAddr == "" && config.DstHostname == "" {
            slog.Error("Skipping tunnel due to missing field", "index", i, "reason", "missing both dst_addr and dst_hostname")
            continue
        }

        if tunnelIDs[config.TunnelID] {
            slog.Error("Skipping tunnel due to duplicate", "index", i, "tunnel_id", config.TunnelID)
            continue
        }
        if dstAddrs[config.DstAddr] {
            slog.Error("Skipping tunnel due to duplicate", "index", i, "dst_addr", config.DstAddr)
            continue
        }
        if vlanIDs[config.VlanID] {
            slog.Error("Skipping tunnel due to duplicate", "index", i, "vlan_id", config.VlanID)
            continue
        }

        validConfigs = append(validConfigs, config)
        tunnelIDs[config.TunnelID] = true
        dstAddrs[config.DstAddr] = true
        vlanIDs[config.VlanID] = true
    }

    return validConfigs, nil
}

// membersEqual は2つのメンバーリストが順序に関係なく一致するか確認
func membersEqual(m1, m2 []string) bool {
    if len(m1) != len(m2) {
        return false
    }
    m1Map := make(map[string]bool)
    for _, m := range m1 {
        m1Map[m] = true
    }
    for _, m := range m2 {
        if !m1Map[m] {
            return false
        }
    }
    return true
}

// applyConfig は差分に基づいて設定を適用
func applyConfig(gifsToAdd, gifsToModify, gifsToRemove map[string]InterfaceConfig, bridgesToAdd, bridgesToRemove map[string]BridgeConfig, configs []TunnelConfig, settings Settings,
    currentGifs map[string]InterfaceConfig, currentVLANs map[string]string, currentBridges map[string]BridgeConfig, forceReset bool) {
    vlanToRemove := make(map[string]bool)
    for vlan := range currentVLANs {
        vlanToRemove[vlan] = true
    }

    for gif := range gifsToRemove {
        if err := runCommand("ifconfig", gif, "destroy"); err != nil {
            slog.Error("Failed to remove gif interface", "gif", gif, "error", err)
        }
    }

    for bridge := range bridgesToRemove {
        if err := runCommand("ifconfig", bridge, "destroy"); err != nil {
            slog.Error("Failed to remove bridge interface", "bridge", bridge, "error", err)
        }
    }

    for _, config := range configs {
        gif := fmt.Sprintf("gif%s", config.TunnelID)
        bridge := fmt.Sprintf("bridge%s", config.TunnelID)
        vlanIface := fmt.Sprintf("%s.%s", settings.PhysicalIface, config.VlanID)

        delete(vlanToRemove, vlanIface)

        if current, exists := currentGifs[gif]; exists && !forceReset {
            if current.Src == config.SrcAddr && current.Dst == config.DstAddr && current.IsIPv6 == strings.Contains(config.SrcAddr, ":") && current.Description == config.Description {
                slog.Debug("gif already exists with correct config, skipping", "gif", gif)
            } else {
                tunnelArgs := []string{gif}
                if strings.Contains(config.SrcAddr, ":") {
                    tunnelArgs = append(tunnelArgs, "inet6")
                }
                tunnelArgs = append(tunnelArgs, "tunnel", config.SrcAddr, config.DstAddr)
                if err := runCommand("ifconfig", tunnelArgs...); err != nil {
                    slog.Error("Failed to configure tunnel", "gif", gif, "error", err)
                    continue
                }
                if err := runCommand("ifconfig", gif, "link0"); err != nil {
                    slog.Error("Failed to set link0 on gif", "gif", gif, "error", err)
                }
                if err := runCommand("ifconfig", gif, "up"); err != nil {
                    slog.Error("Failed to bring up gif", "gif", gif, "error", err)
                }
                if config.Description != "" {
                    if err := runCommand("ifconfig", gif, "description", config.Description); err != nil {
                        slog.Error("Failed to update description on gif", "gif", gif, "error", err)
                    } else {
                        slog.Info("Updated gif description", "gif", gif, "description", config.Description)
                    }
                }
            }
        } else {
            if err := runCommand("ifconfig", gif, "create"); err != nil {
                slog.Error("Failed to create gif", "gif", gif, "error", err)
                continue
            }
            tunnelArgs := []string{gif}
            if strings.Contains(config.SrcAddr, ":") {
                tunnelArgs = append(tunnelArgs, "inet6")
            }
            tunnelArgs = append(tunnelArgs, "tunnel", config.SrcAddr, config.DstAddr)
            if err := runCommand("ifconfig", tunnelArgs...); err != nil {
                slog.Error("Failed to configure tunnel", "gif", gif, "error", err)
                continue
            }
            if err := runCommand("ifconfig", gif, "mtu", "1500"); err != nil {
                slog.Error("Failed to set MTU on gif", "gif", gif, "error", err)
            }
            if err := runCommand("ifconfig", gif, "link0"); err != nil {
                slog.Error("Failed to set link0 on gif", "gif", gif, "error", err)
            }
            if err := runCommand("ifconfig", gif, "up"); err != nil {
                slog.Error("Failed to bring up gif", "gif", gif, "error", err)
            }
            if config.Description != "" {
                if err := runCommand("ifconfig", gif, "description", config.Description); err != nil {
                    slog.Error("Failed to set description on gif", "gif", gif, "error", err)
                } else {
                    slog.Info("Set gif description", "gif", gif, "description", config.Description)
                }
            }
        }

        if vlanID, exists := currentVLANs[vlanIface]; exists && vlanID == config.VlanID && !forceReset {
            slog.Debug("VLAN already exists with correct config, skipping", "vlan", vlanIface)
        } else {
            if _, exists := currentVLANs[vlanIface]; exists {
                if err := runCommand("ifconfig", vlanIface, "destroy"); err != nil {
                    slog.Error("Failed to remove VLAN for reconfiguration", "vlan", vlanIface, "error", err)
                    continue
                }
            }
            if err := runCommand("ifconfig", vlanIface, "create"); err != nil {
                slog.Error("Failed to create VLAN", "vlan", vlanIface, "error", err)
                continue
            }
            if err := runCommand("ifconfig", vlanIface, "vlan", config.VlanID, "vlandev", settings.PhysicalIface, "up"); err != nil {
                slog.Error("Failed to configure VLAN", "vlan", vlanIface, "error", err)
            }
        }

        expectedMembers := []string{gif, vlanIface}
        if current, exists := currentBridges[bridge]; exists && !forceReset {
            if membersEqual(current.Members, expectedMembers) {
                slog.Debug("bridge already exists with correct config, skipping", "bridge", bridge)
            } else {
                if err := runCommand("ifconfig", bridge, "destroy"); err != nil {
                    slog.Error("Failed to remove bridge for reconfiguration", "bridge", bridge, "error", err)
                    continue
                }
                if err := runCommand("ifconfig", bridge, "create"); err != nil {
                    slog.Error("Failed to create bridge", "bridge", bridge, "error", err)
                    continue
                }
                for _, member := range expectedMembers {
                    if err := runCommand("ifconfig", bridge, "addm", member); err != nil {
                        slog.Error("Failed to add member to bridge", "member", member, "bridge", bridge, "error", err)
                    }
                }
                if err := runCommand("ifconfig", bridge, "up"); err != nil {
                    slog.Error("Failed to bring up bridge", "bridge", bridge, "error", err)
                }
            }
        } else {
            if err := runCommand("ifconfig", bridge, "create"); err != nil {
                slog.Error("Failed to create bridge", "bridge", bridge, "error", err)
                continue
            }
            for _, member := range expectedMembers {
                if err := runCommand("ifconfig", bridge, "addm", member); err != nil {
                    slog.Error("Failed to add member to bridge", "member", member, "bridge", bridge, "error", err)
                }
            }
            if err := runCommand("ifconfig", bridge, "mtu", "1500"); err != nil {
                slog.Error("Failed to set MTU on bridge", "bridge", bridge, "error", err)
            }
            if err := runCommand("ifconfig", bridge, "up"); err != nil {
                slog.Error("Failed to bring up bridge", "bridge", bridge, "error", err)
            }
        }
    }

    for vlan := range vlanToRemove {
        if err := runCommand("ifconfig", vlan, "destroy"); err != nil {
            slog.Error("Failed to remove unused VLAN", "vlan", vlan, "error", err)
        }
    }
}

// calculateDiff は現在の状態とJSONデータの差分を計算
func calculateDiff(currentGifs map[string]InterfaceConfig, currentBridges map[string]BridgeConfig, configs []TunnelConfig, physicalIface string) (
    gifsToAdd, gifsToModify, gifsToRemove map[string]InterfaceConfig, bridgesToAdd, bridgesToRemove map[string]BridgeConfig) {
    jsonGifs := make(map[string]InterfaceConfig)
    jsonBridges := make(map[string]BridgeConfig)

    for _, config := range configs {
        gif := fmt.Sprintf("gif%s", config.TunnelID)
        bridge := fmt.Sprintf("bridge%s", config.TunnelID)
        isIPv6 := strings.Contains(config.SrcAddr, ":") || strings.Contains(config.DstAddr, ":")
        jsonGifs[gif] = InterfaceConfig{Src: config.SrcAddr, Dst: config.DstAddr, Vlan: config.VlanID, IsIPv6: isIPv6, TunnelID: config.TunnelID, Description: config.Description}
        jsonBridges[bridge] = BridgeConfig{
            Members:  []string{gif, fmt.Sprintf("%s.%s", physicalIface, config.VlanID)},
            TunnelID: config.TunnelID,
        }
    }

    gifsToAdd = make(map[string]InterfaceConfig)
    gifsToModify = make(map[string]InterfaceConfig)
    gifsToRemove = make(map[string]InterfaceConfig)
    bridgesToAdd = make(map[string]BridgeConfig)
    bridgesToRemove = make(map[string]BridgeConfig)

    for k, v := range jsonGifs {
        if current, exists := currentGifs[k]; exists {
            if current.Src != v.Src || current.Dst != v.Dst || current.IsIPv6 != v.IsIPv6 || current.Description != v.Description {
                gifsToModify[k] = v
            }
        } else {
            gifsToAdd[k] = v
        }
    }
    for k, v := range currentGifs {
        if _, exists := jsonGifs[k]; !exists {
            gifsToRemove[k] = v
        }
    }
    for k, v := range jsonBridges {
        if _, exists := currentBridges[k]; !exists || !membersEqual(currentBridges[k].Members, v.Members) {
            bridgesToAdd[k] = v
        }
    }
    for k, v := range currentBridges {
        if _, exists := jsonBridges[k]; !exists {
            bridgesToRemove[k] = v
        }
    }

    return
}

// waitForInterfacesRemoval は指定されたインターフェイス群がすべて削除されるのを待機
func waitForInterfacesRemoval(interfaces []string) error {
    slog.Debug("Starting interface removal check", "interfaces", interfaces)
    timeout := 10 * time.Second
    interval := 50 * time.Millisecond
    remaining := []string{}
    start := time.Now()

    for time.Since(start) < timeout {
        output, err := exec.Command("ifconfig", "-a").Output()
        if err != nil {
            slog.Warn("Failed to check interfaces removal", "error", err)
            time.Sleep(interval)
            continue
        }
        outputStr := string(output)
        remaining := []string{}
        for _, iface := range interfaces {
            if strings.Contains(outputStr, iface) {
                remaining = append(remaining, iface)
            }
        }
        if len(remaining) == 0 {
            slog.Debug("All interfaces removed successfully", "interfaces", interfaces)
            return nil
        }
        time.Sleep(interval)
    }

    slog.Warn("Timeout waiting for interfaces removal", "remaining", remaining, "timeout", timeout)
    return fmt.Errorf("some interfaces still exist after %v: %v", timeout, remaining)
}

// resetVLANs は物理インターフェイスのVLANをすべて削除
func resetVLANs(physicalIface string, currentVLANs map[string]string) error {
    var vlansToRemove []string
    for vlan := range currentVLANs {
        if strings.HasPrefix(vlan, physicalIface+".") {
            if err := runCommand("ifconfig", vlan, "destroy"); err != nil {
                slog.Error("Failed to remove VLAN during reset", "vlan", vlan, "error", err)
                return err
            }
            vlansToRemove = append(vlansToRemove, vlan)
        }
    }

    if len(vlansToRemove) > 0 {
        if err := waitForInterfacesRemoval(vlansToRemove); err != nil {
            slog.Error("Failed to confirm VLANs removal", "vlans", vlansToRemove, "error", err)
            return err
        }
    }

    slog.Info("Successfully reset VLANs for physical interface", "physical_iface", physicalIface)
    return nil
}

// resetAllInterfaces はトンネル、VLAN、ブリッジをすべて削除
func resetAllInterfaces(currentGifs map[string]InterfaceConfig, currentVLANs map[string]string, currentBridges map[string]BridgeConfig) error {
    var interfacesToRemove []string

    for gif := range currentGifs {
        if err := runCommand("ifconfig", gif, "destroy"); err != nil {
            slog.Error("Failed to remove GIF tunnel during reset", "gif", gif, "error", err)
            return err
        }
        interfacesToRemove = append(interfacesToRemove, gif)
    }

    for vlan := range currentVLANs {
        if err := runCommand("ifconfig", vlan, "destroy"); err != nil {
            slog.Error("Failed to remove VLAN during reset", "vlan", vlan, "error", err)
            return err
        }
        interfacesToRemove = append(interfacesToRemove, vlan)
    }

    for bridge := range currentBridges {
        if err := runCommand("ifconfig", bridge, "destroy"); err != nil {
            slog.Error("Failed to remove bridge during reset", "bridge", bridge, "error", err)
            return err
        }
        interfacesToRemove = append(interfacesToRemove, bridge)
    }

    if len(interfacesToRemove) > 0 {
        if err := waitForInterfacesRemoval(interfacesToRemove); err != nil {
            slog.Error("Failed to confirm all interfaces removal", "interfaces", interfacesToRemove, "error", err)
            return err
        }
    }

    slog.Info("Successfully reset all interfaces (GIF tunnels, VLANs, and bridges)")
    return nil
}

// main は定期的に設定ファイルから設定を読み込み、トンネル設定をフェッチして適用
func main() {
    exe, err := os.Executable()
    defaultSettingsFile := filepath.Dir(exe) + "/settings.json"

    var settingsFile string
    flag.StringVar(&settingsFile, "config", defaultSettingsFile, "Path to settings.json file")
    flag.StringVar(&settingsFile, "c", defaultSettingsFile, "Short form of --config")
    flag.Parse()

    if envSettingsFile := os.Getenv("EIPCONF_CONF"); envSettingsFile != "" && settingsFile == defaultSettingsFile {
        settingsFile = envSettingsFile
    }

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)

    settings, err := loadSettings(settingsFile)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Initial load settings failed: %v\n", err)
        os.Exit(1)
    }

    var logLevel slog.Level
    switch strings.ToUpper(settings.LogLevel) {
    case "DEBUG":
        logLevel = slog.LevelDebug
    case "WARN":
        logLevel = slog.LevelWarn
    case "ERROR":
        logLevel = slog.LevelError
    case "INFO", "":
        logLevel = slog.LevelInfo
    default:
        fmt.Fprintf(os.Stderr, "Invalid log_level: %s, defaulting to INFO\n", settings.LogLevel)
        logLevel = slog.LevelInfo
    }

    consoleHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
        AddSource: false,
        Level:     logLevel,
    })
    var handler slog.Handler = consoleHandler

    if settings.LogFile != "" {
        logFile, err := os.OpenFile(settings.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Failed to open log file %s: %v, using console only\n", settings.LogFile, err)
        } else {
            fileHandler := slog.NewTextHandler(logFile, &slog.HandlerOptions{
                AddSource: false,
                Level:     logLevel,
            })
            handler = slogmultiHandler{consoleHandler, fileHandler}
        }
    }

    slog.SetDefault(slog.New(&SlackHandler{
        Handler:  handler,
        Settings: &settings,
    }))

    slog.Info("Program start.")

    defer func() {
        if r := recover(); r != nil {
            slog.Error("Program terminated due to panic", "reason", fmt.Sprintf("%v", r))
            os.Exit(2)
        }
    }()

    interval := time.Duration(settings.FetchInterval) * time.Second

    done := make(chan struct{})
    var exitReason string
    var exitCode int

    go func() {
        fail_interval := 5
        for {
            select {
            case <-done:
                return
            default:
                currentGifs, currentBridges, currentVLANs := getCurrentInterfaces()
                configs, err := fetchConfig(settings.ConfigSource, currentGifs, settings)
                if err != nil {
                    slog.Error("Failed to fetch config", "source", settings.ConfigSource, "error", err)
                    time.Sleep(time.Duration(fail_interval) * time.Second)
                    fail_interval += 5
                    continue
                }

                fail_interval = 5

                gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove := calculateDiff(currentGifs, currentBridges, configs, settings.PhysicalIface)
                notifyConfigDiff(gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove, &settings)
                applyConfig(gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove, configs, settings, currentGifs, currentVLANs, currentBridges, false)

                slog.Info("Configuration check completed", "sleep", interval)
                time.Sleep(interval)
            }
        }
    }()

    for {
        sig := <-sigChan
        switch sig {
        case syscall.SIGHUP:
            slog.Info("Received SIGHUP, forcing immediate config update")
            currentGifs, currentBridges, currentVLANs := getCurrentInterfaces()
            configs, err := fetchConfig(settings.ConfigSource, currentGifs, settings)
            if err != nil {
                slog.Error("Failed to fetch config after SIGHUP", "source", settings.ConfigSource, "error", err)
            } else {
                gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove := calculateDiff(currentGifs, currentBridges, configs, settings.PhysicalIface)
                notifyConfigDiff(gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove, &settings)
                applyConfig(gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove, configs, settings, currentGifs, currentVLANs, currentBridges, false)
                slog.Info("Immediate config update completed after SIGHUP")
            }
        case syscall.SIGUSR1:
            slog.Info("Received SIGUSR1, resetting VLANs")
            currentGifs, _, currentVLANs := getCurrentInterfaces()
            if err := resetVLANs(settings.PhysicalIface, currentVLANs); err == nil {
                configs, err := fetchConfig(settings.ConfigSource, currentGifs, settings)
                if err != nil {
                    slog.Error("Failed to fetch config after SIGUSR1", "source", settings.ConfigSource, "error", err)
                } else {
                    currentGifs, currentBridges, currentVLANs := getCurrentInterfaces()
                    gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove := calculateDiff(currentGifs, currentBridges, configs, settings.PhysicalIface)
                    notifyConfigDiff(gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove, &settings)
                    applyConfig(gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove, configs, settings, currentGifs, currentVLANs, currentBridges, false)
                    slog.Info("Reconfiguration completed after SIGUSR1")
                }
            }
        case syscall.SIGUSR2:
            slog.Info("Received SIGUSR2, resetting all interfaces")
            currentGifs, currentBridges, currentVLANs := getCurrentInterfaces()
            if err := resetAllInterfaces(currentGifs, currentVLANs, currentBridges); err == nil {
                configs, err := fetchConfig(settings.ConfigSource, currentGifs, settings)
                if err != nil {
                    slog.Error("Failed to fetch config after SIGUSR2", "source", settings.ConfigSource, "error", err)
                } else {
                    currentGifs, currentBridges, currentVLANs := getCurrentInterfaces()
                    gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove := calculateDiff(currentGifs, currentBridges, configs, settings.PhysicalIface)
                    notifyConfigDiff(gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove, &settings)
                    applyConfig(gifsToAdd, gifsToModify, gifsToRemove, bridgesToAdd, bridgesToRemove, configs, settings, currentGifs, currentVLANs, currentBridges, false)
                    slog.Info("Reconfiguration completed after SIGUSR2")
                }
            }
        case syscall.SIGINT, syscall.SIGTERM:
            close(done)
            exitReason = fmt.Sprintf("terminated by signal: %v", sig)
            exitCode = 0
            slog.Info("Program terminated", "reason", exitReason, "exit_code", exitCode)
            os.Exit(exitCode)
        }
    }
}

// slogmultiHandler は複数のハンドラを組み合わせるための簡易実装
type slogmultiHandler []slog.Handler

func (h slogmultiHandler) Handle(ctx context.Context, r slog.Record) error {
    for _, handler := range h {
        if err := handler.Handle(ctx, r); err != nil {
            return err
        }
    }
    return nil
}

func (h slogmultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
    for _, handler := range h {
        if handler.Enabled(ctx, level) {
            return true
        }
    }
    return false
}

func (h slogmultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
    handlers := make([]slog.Handler, len(h))
    for i, handler := range h {
        handlers[i] = handler.WithAttrs(attrs)
    }
    return slogmultiHandler(handlers)
}

func (h slogmultiHandler) WithGroup(name string) slog.Handler {
    handlers := make([]slog.Handler, len(h))
    for i, handler := range h {
        handlers[i] = handler.WithGroup(name)
    }
    return slogmultiHandler(handlers)
}
