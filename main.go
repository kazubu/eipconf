package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io/ioutil"
    "log/slog"
    "net/http"
    "os"
    "os/exec"
    "regexp"
    "strings"
    "time"
)

// Settings はローカル設定ファイルから読み込む構造体
type Settings struct {
    URL             string `json:"url"`
    PhysicalIface   string `json:"physical_iface"`
    SlackWebhookURL string `json:"slack_webhook_url,omitempty"`
}

// TunnelConfig はJSONから読み込むトンネル設定を表す構造体
type TunnelConfig struct {
    TunnelID string `json:"tunnel_id"`
    SrcAddr  string `json:"src_addr"`
    DstAddr  string `json:"dst_addr"`
    VlanID   string `json:"vlan_id"`
}

// InterfaceConfig はインターフェースの設定を表す構造体
type InterfaceConfig struct {
    Src      string
    Dst      string
    Vlan     string
    IsIPv6   bool
    TunnelID string
}

// BridgeConfig はブリッジの設定を表す構造体
type BridgeConfig struct {
    Members  []string
    TunnelID string
}

// SlackHandler はWARN以上のログをSlackに送信するカスタムハンドラ
type SlackHandler struct {
    slog.Handler
    SlackWebhookURL string
}

func (h *SlackHandler) Handle(r slog.Record) error {
    err := h.Handler.Handle(r)
    if r.Level >= slog.LevelWarn && h.SlackWebhookURL != "" {
        go sendToSlack(r, h.SlackWebhookURL)
    }
    return err
}

func sendToSlack(r slog.Record, webhookURL string) {
    hostname, err := os.Hostname()
    if err != nil {
        hostname = "unknown"
    }

    // Slackメッセージの構築（ホスト名を追加）
    msg := fmt.Sprintf("[%s] %s: %s", hostname, r.Level.String(), r.Message)
    var attrs []slog.Attr
    r.Attrs(func(a slog.Attr) bool {
        attrs = append(attrs, a)
        return true
    })
    for _, attr := range attrs {
        msg += fmt.Sprintf(" %s=%v", attr.Key, attr.Value)
    }

    payload := map[string]string{
        "text": msg,
    }
    payloadBytes, _ := json.Marshal(payload)

    // 非同期でSlackに送信
    resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(payloadBytes))
    if err != nil {
        fmt.Fprintf(os.Stderr, "Failed to send to Slack: %v\n", err)
        return
    }
    defer resp.Body.Close()
}

// notifyConfigDiff は差分をSlackに通知
func notifyConfigDiff(gifsToAdd, gifsToRemove map[string]InterfaceConfig, bridgesToAdd, bridgesToRemove map[string]BridgeConfig, webhookURL string) {
    if len(gifsToAdd) == 0 && len(gifsToRemove) == 0 && len(bridgesToAdd) == 0 && len(bridgesToRemove) == 0 {
        return // 差分がない場合は通知しない
    }

    hostname, err := os.Hostname()
    if err != nil {
        hostname = "unknown"
    }

    var msg strings.Builder
    msg.WriteString(fmt.Sprintf("[%s] Configuration updated:\n", hostname))

    // 追加されたトンネル
    if len(gifsToAdd) > 0 {
        msg.WriteString("Added tunnels:\n")
        for _, config := range gifsToAdd {
            msg.WriteString(fmt.Sprintf("- tunnel_id=%s, src_addr=%s, dst_addr=%s, vlan_id=%s\n", config.TunnelID, config.Src, config.Dst, config.Vlan))
        }
    }

    // 削除されたトンネル
    if len(gifsToRemove) > 0 {
        msg.WriteString("Removed tunnels:\n")
        for _, config := range gifsToRemove {
            msg.WriteString(fmt.Sprintf("- tunnel_id=%s, src_addr=%s, dst_addr=%s, vlan_id=%s\n", config.TunnelID, config.Src, config.Dst, config.Vlan))
        }
    }

    payload := map[string]string{
        "text": msg.String(),
    }
    payloadBytes, _ := json.Marshal(payload)

    // 非同期でSlackに送信
    resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(payloadBytes))
    if err != nil {
        fmt.Fprintf(os.Stderr, "Failed to send config diff to Slack: %v\n", err)
        return
    }
    defer resp.Body.Close()
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
                srcDstIPv4 := regexp.MustCompile(`tunnel inet (\S+) --> (\S+)`).FindStringSubmatch(detailStr)
                srcDstIPv6 := regexp.MustCompile(`tunnel inet6 (\S+) --> (\S+)`).FindStringSubmatch(detailStr)
                tunnelID := strings.TrimPrefix(gifName, "gif")
                if len(srcDstIPv4) == 3 {
                    gifInterfaces[gifName] = InterfaceConfig{Src: srcDstIPv4[1], Dst: srcDstIPv4[2], Vlan: "", IsIPv6: false, TunnelID: tunnelID}
                } else if len(srcDstIPv6) == 3 {
                    gifInterfaces[gifName] = InterfaceConfig{Src: srcDstIPv6[1], Dst: srcDstIPv6[2], Vlan: "", IsIPv6: true, TunnelID: tunnelID}
                } else {
                    gifInterfaces[gifName] = InterfaceConfig{Src: "", Dst: "", Vlan: "", IsIPv6: false, TunnelID: tunnelID}
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

// loadSettings はローカルの設定ファイルからURLとPhysicalIfaceを読み込む
func loadSettings(filename string) (Settings, error) {
    data, err := ioutil.ReadFile(filename)
    if err != nil {
        return Settings{}, fmt.Errorf("failed to read settings file: %v", err)
    }

    var settings Settings
    if err := json.Unmarshal(data, &settings); err != nil {
        return Settings{}, fmt.Errorf("failed to unmarshal settings: %v", err)
    }

    if settings.URL == "" || settings.PhysicalIface == "" {
        return Settings{}, fmt.Errorf("URL or PhysicalIface is not specified in settings file")
    }

    // 環境変数が優先、未設定ならsettings.jsonの値を使用
    if envURL := os.Getenv("SLACK_WEBHOOK_URL"); envURL != "" {
        settings.SlackWebhookURL = envURL
    }

    return settings, nil
}

// fetchJSON は指定されたURLからJSONデータを取得し、重複と欠落をチェック
func fetchJSON(url string) ([]TunnelConfig, error) {
    resp, err := http.Get(url)
    if err != nil {
        return nil, fmt.Errorf("failed to fetch JSON: %v", err)
    }
    defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("failed to read response body: %v", err)
    }

    var configs []TunnelConfig
    if err := json.Unmarshal(body, &configs); err != nil {
        return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
    }

    // 有効な設定のみを収集
    var validConfigs []TunnelConfig
    tunnelIDs := make(map[string]bool)
    dstAddrs := make(map[string]bool)
    vlanIDs := make(map[string]bool)

    for i, config := range configs {
        // 欠落チェック
        if config.TunnelID == "" {
            slog.Error("Skipping tunnel due to missing field", "index", i, "reason", "missing tunnel_id")
            continue
        }
        if config.SrcAddr == "" {
            slog.Error("Skipping tunnel due to missing field", "index", i, "reason", "missing src_addr")
            continue
        }
        if config.DstAddr == "" {
            slog.Error("Skipping tunnel due to missing field", "index", i, "reason", "missing dst_addr")
            continue
        }
        if config.VlanID == "" {
            slog.Error("Skipping tunnel due to missing field", "index", i, "reason", "missing vlan_id")
            continue
        }

        // 重複チェック
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

        // 有効な設定として追加
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
func applyConfig(gifsToAdd, gifsToRemove map[string]InterfaceConfig, bridgesToAdd, bridgesToRemove map[string]BridgeConfig, configs []TunnelConfig, settings Settings,
    currentGifs map[string]InterfaceConfig, currentVLANs map[string]string, currentBridges map[string]BridgeConfig) {
    // 削除対象の収集
    vlanToRemove := make(map[string]bool)
    for vlan := range currentVLANs {
        vlanToRemove[vlan] = true
    }

    // gifインターフェースの削除
    for gif := range gifsToRemove {
        if err := runCommand("ifconfig", gif, "destroy"); err != nil {
            slog.Error("Failed to remove gif interface", "gif", gif, "error", err)
        }
    }

    // bridgeインターフェースの削除
    for bridge := range bridgesToRemove {
        if err := runCommand("ifconfig", bridge, "destroy"); err != nil {
            slog.Error("Failed to remove bridge interface", "bridge", bridge, "error", err)
        }
    }

    // gifインターフェースとVLAN、bridgeの追加と設定
    for _, config := range configs {
        gif := fmt.Sprintf("gif%s", config.TunnelID)
        bridge := fmt.Sprintf("bridge%s", config.TunnelID)
        vlanIface := fmt.Sprintf("%s.%s", settings.PhysicalIface, config.VlanID)

        // VLANから削除対象を除外
        delete(vlanToRemove, vlanIface)

        // gifインターフェース
        if current, exists := currentGifs[gif]; exists {
            if current.Src == config.SrcAddr && current.Dst == config.DstAddr && current.IsIPv6 == strings.Contains(config.SrcAddr, ":") {
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
                if err := runCommand("ifconfig", gif, "up"); err != nil {
                    slog.Error("Failed to bring up gif", "gif", gif, "error", err)
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
            if err := runCommand("ifconfig", gif, "up"); err != nil {
                slog.Error("Failed to bring up gif", "gif", gif, "error", err)
            }
        }

        // VLANの設定
        if vlanID, exists := currentVLANs[vlanIface]; exists && vlanID == config.VlanID {
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

        // bridgeインターフェース
        expectedMembers := []string{gif, vlanIface}
        if current, exists := currentBridges[bridge]; exists {
            if membersEqual(current.Members, expectedMembers) {
                slog.Debug("bridge already exists with correct config, skipping", "bridge", bridge)
                continue
            }
            if err := runCommand("ifconfig", bridge, "destroy"); err != nil {
                slog.Error("Failed to remove bridge for reconfiguration", "bridge", bridge, "error", err)
                continue
            }
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

    // 未使用のVLANインターフェースの削除
    for vlan := range vlanToRemove {
        if err := runCommand("ifconfig", vlan, "destroy"); err != nil {
            slog.Error("Failed to remove unused VLAN", "vlan", vlan, "error", err)
        }
    }
}

// calculateDiff は現在の状態とJSONデータの差分を計算
func calculateDiff(currentGifs map[string]InterfaceConfig, currentBridges map[string]BridgeConfig, configs []TunnelConfig, physicalIface string) (
    map[string]InterfaceConfig, map[string]InterfaceConfig, map[string]BridgeConfig, map[string]BridgeConfig) {
    jsonGifs := make(map[string]InterfaceConfig)
    jsonBridges := make(map[string]BridgeConfig)

    for _, config := range configs {
        gif := fmt.Sprintf("gif%s", config.TunnelID)
        bridge := fmt.Sprintf("bridge%s", config.TunnelID)
        isIPv6 := strings.Contains(config.SrcAddr, ":") || strings.Contains(config.DstAddr, ":")
        jsonGifs[gif] = InterfaceConfig{Src: config.SrcAddr, Dst: config.DstAddr, Vlan: config.VlanID, IsIPv6: isIPv6, TunnelID: config.TunnelID}
        jsonBridges[bridge] = BridgeConfig{
            Members:  []string{gif, fmt.Sprintf("%s.%s", physicalIface, config.VlanID)},
            TunnelID: config.TunnelID,
        }
    }

    gifsToAdd := make(map[string]InterfaceConfig)
    gifsToRemove := make(map[string]InterfaceConfig)
    bridgesToAdd := make(map[string]BridgeConfig)
    bridgesToRemove := make(map[string]BridgeConfig)

    for k, v := range jsonGifs {
        if _, exists := currentGifs[k]; !exists || (currentGifs[k].Src != v.Src || currentGifs[k].Dst != v.Dst || currentGifs[k].IsIPv6 != v.IsIPv6) {
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

    return gifsToAdd, gifsToRemove, bridgesToAdd, bridgesToRemove
}

// main は定期的にローカル設定ファイルからURLを読み込み、JSONをフェッチして設定を更新
func main() {
    settingsFile := "settings.json"
    interval := 30 * time.Second

    // 設定の読み込み（初回のみ）
    settings, err := loadSettings(settingsFile)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Initial load settings failed: %v\n", err)
        return
    }

    // slogの設定（デフォルトでINFO以上を表示）
    slog.SetDefault(slog.New(&SlackHandler{
        Handler: slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
            AddSource: false,
            Level:     slog.LevelInfo, // DEBUGはデフォルトで非表示
        }),
        SlackWebhookURL: settings.SlackWebhookURL,
    }))

    for {
        currentGifs, currentBridges, currentVLANs := getCurrentInterfaces()
        configs, err := fetchJSON(settings.URL)
        if err != nil {
            slog.Error("Failed to fetch JSON", "url", settings.URL, "error", err)
            time.Sleep(interval)
            continue
        }

        gifsToAdd, gifsToRemove, bridgesToAdd, bridgesToRemove := calculateDiff(currentGifs, currentBridges, configs, settings.PhysicalIface)
        // 差分があればSlackに通知
        if settings.SlackWebhookURL != "" {
            notifyConfigDiff(gifsToAdd, gifsToRemove, bridgesToAdd, bridgesToRemove, settings.SlackWebhookURL)
        }
        applyConfig(gifsToAdd, gifsToRemove, bridgesToAdd, bridgesToRemove, configs, settings, currentGifs, currentVLANs, currentBridges)

        slog.Info("Configuration check completed", "sleep", interval)
        time.Sleep(interval)
    }
}
