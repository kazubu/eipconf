package main

import (
    "encoding/json"
    "fmt"
    "io/ioutil"
    "log"
    "net/http"
    "os/exec"
    "regexp"
    "strings"
    "time"
)

// Settings はローカル設定ファイルから読み込む構造体
type Settings struct {
    URL           string `json:"url"`
    PhysicalIface string `json:"physical_iface"`
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
    TunnelID string // 生成元となるtunnel_idを記録
}

// BridgeConfig はブリッジの設定を表す構造体
type BridgeConfig struct {
    Members  []string
    TunnelID string // 生成元となるtunnel_idを記録
}

// runCommand はコマンドを実行し、エラーがあれば再試行する
func runCommand(cmd string, args ...string) error {
    for attempt := 0; attempt < 3; attempt++ {
        command := exec.Command(cmd, args...)
        output, err := command.CombinedOutput()
        if err == nil {
            log.Printf("Command succeeded: %s %v", cmd, args)
            return nil
        }
        outputStr := string(output)
        if strings.Contains(outputStr, "already exists") {
            log.Printf("Interface already exists, skipping creation: %s %v", cmd, args)
            return nil
        }
        log.Printf("Command failed: %s %v, Error: %s", cmd, args, outputStr)
        if attempt < 2 {
            log.Printf("Retrying in 1 second...")
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
        log.Printf("Failed to get current interfaces: %v", err)
        return gifInterfaces, bridgeInterfaces, vlanInterfaces
    }

    lines := strings.Split(string(output), "\n")
    for _, line := range lines {
        // gifインターフェース
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
        // bridgeインターフェース
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
        // VLANインターフェース
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

    return settings, nil
}

// fetchJSON は指定されたURLからJSONデータを取得
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
    return configs, nil
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
            log.Printf("Failed to remove gif interface %s: %v", gif, err)
        }
    }

    // bridgeインターフェースの削除
    for bridge := range bridgesToRemove {
        if err := runCommand("ifconfig", bridge, "destroy"); err != nil {
            log.Printf("Failed to remove bridge interface %s: %v", bridge, err)
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
                log.Printf("gif %s already exists with correct config, skipping", gif)
            } else {
                tunnelArgs := []string{gif}
                if strings.Contains(config.SrcAddr, ":") {
                    tunnelArgs = append(tunnelArgs, "inet6")
                }
                tunnelArgs = append(tunnelArgs, "tunnel", config.SrcAddr, config.DstAddr)
                if err := runCommand("ifconfig", tunnelArgs...); err != nil {
                    log.Printf("Failed to configure tunnel for %s: %v", gif, err)
                    continue
                }
                if err := runCommand("ifconfig", gif, "up"); err != nil {
                    log.Printf("Failed to bring up gif %s: %v", gif, err)
                }
            }
        } else {
            if err := runCommand("ifconfig", gif, "create"); err != nil {
                log.Printf("Failed to create gif %s: %v", gif, err)
                continue
            }
            tunnelArgs := []string{gif}
            if strings.Contains(config.SrcAddr, ":") {
                tunnelArgs = append(tunnelArgs, "inet6")
            }
            tunnelArgs = append(tunnelArgs, "tunnel", config.SrcAddr, config.DstAddr)
            if err := runCommand("ifconfig", tunnelArgs...); err != nil {
                log.Printf("Failed to configure tunnel for %s: %v", gif, err)
                continue
            }
            if err := runCommand("ifconfig", gif, "up"); err != nil {
                log.Printf("Failed to bring up gif %s: %v", gif, err)
            }
        }

        // VLANの設定
        if vlanID, exists := currentVLANs[vlanIface]; exists && vlanID == config.VlanID {
            log.Printf("VLAN %s already exists with correct config, skipping", vlanIface)
        } else {
            if _, exists := currentVLANs[vlanIface]; exists {
                if err := runCommand("ifconfig", vlanIface, "destroy"); err != nil {
                    log.Printf("Failed to remove VLAN %s for reconfiguration: %v", vlanIface, err)
                    continue
                }
            }
            if err := runCommand("ifconfig", vlanIface, "create"); err != nil {
                log.Printf("Failed to create VLAN %s: %v", vlanIface, err)
                continue
            }
            if err := runCommand("ifconfig", vlanIface, "vlan", config.VlanID, "vlandev", settings.PhysicalIface, "up"); err != nil {
                log.Printf("Failed to configure VLAN %s: %v", vlanIface, err)
            }
        }

        // bridgeインターフェース
        if current, exists := currentBridges[bridge]; exists {
            if len(current.Members) == 2 && current.Members[0] == gif && current.Members[1] == vlanIface {
                log.Printf("bridge %s already exists with correct config, skipping", bridge)
                continue
            }
            if err := runCommand("ifconfig", bridge, "destroy"); err != nil {
                log.Printf("Failed to remove bridge %s for reconfiguration: %v", bridge, err)
                continue
            }
        }
        if err := runCommand("ifconfig", bridge, "create"); err != nil {
            log.Printf("Failed to create bridge %s: %v", bridge, err)
            continue
        }
        for _, member := range []string{gif, vlanIface} {
            if err := runCommand("ifconfig", bridge, "addm", member); err != nil {
                log.Printf("Failed to add member %s to bridge %s: %v", member, bridge, err)
            }
        }
        if err := runCommand("ifconfig", bridge, "up"); err != nil {
            log.Printf("Failed to bring up bridge %s: %v", bridge, err)
        }
    }

    // 未使用のVLANインターフェースの削除
    for vlan := range vlanToRemove {
        if err := runCommand("ifconfig", vlan, "destroy"); err != nil {
            log.Printf("Failed to remove unused VLAN %s: %v", vlan, err)
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
        if _, exists := currentBridges[k]; !exists {
            bridgesToAdd[k] = v
        } else {
            current := currentBridges[k]
            if len(current.Members) != len(v.Members) || current.Members[0] != v.Members[0] || current.Members[1] != v.Members[1] {
                bridgesToAdd[k] = v
            }
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

    for {
        settings, err := loadSettings(settingsFile)
        if err != nil {
            log.Printf("Failed to load settings: %v", err)
            time.Sleep(interval)
            continue
        }

        currentGifs, currentBridges, currentVLANs := getCurrentInterfaces()
        configs, err := fetchJSON(settings.URL)
        if err != nil {
            log.Printf("Failed to fetch JSON from %s: %v", settings.URL, err)
            time.Sleep(interval)
            continue
        }

        gifsToAdd, gifsToRemove, bridgesToAdd, bridgesToRemove := calculateDiff(currentGifs, currentBridges, configs, settings.PhysicalIface)
        applyConfig(gifsToAdd, gifsToRemove, bridgesToAdd, bridgesToRemove, configs, settings, currentGifs, currentVLANs, currentBridges)

        log.Printf("Configuration check completed. Sleeping for %v...", interval)
        time.Sleep(interval)
    }
}
