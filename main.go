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
    URL string `json:"url"`
}

// TunnelConfig はJSONから読み込むトンネル設定を表す構造体
type TunnelConfig struct {
    Gif           string `json:"gif"`
    SrcAddr       string `json:"src_addr"`
    DstAddr       string `json:"dst_addr"`
    VlanID        string `json:"vlan_id"`
    Bridge        string `json:"bridge"`
    PhysicalIface string `json:"physical_iface"`
}

// InterfaceConfig はインターフェースの設定を表す構造体
type InterfaceConfig struct {
    Src   string
    Dst   string
    Vlan  string
}

// BridgeConfig はブリッジの設定を表す構造体
type BridgeConfig struct {
    Members []string
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
        log.Printf("Command failed: %s %v, Error: %s", cmd, args, output)
        if attempt < 2 {
            log.Printf("Retrying in 1 second...")
            time.Sleep(time.Second)
        }
    }
    return fmt.Errorf("command failed after 3 attempts: %s %v", cmd, args)
}

// getCurrentInterfaces は現在のgifおよびbridgeインターフェースを取得
func getCurrentInterfaces() (map[string]InterfaceConfig, map[string]BridgeConfig) {
    gifInterfaces := make(map[string]InterfaceConfig)
    bridgeInterfaces := make(map[string]BridgeConfig)

    output, err := exec.Command("ifconfig", "-a").Output()
    if err != nil {
        log.Printf("Failed to get current interfaces: %v", err)
        return gifInterfaces, bridgeInterfaces
    }

    lines := strings.Split(string(output), "\n")
    for _, line := range lines {
        if strings.Contains(line, "gif") {
            gifName := regexp.MustCompile(`gif\d+`).FindString(line)
            if gifName != "" {
                detail, _ := exec.Command("ifconfig", gifName).Output()
                srcDst := regexp.MustCompile(`tunnel inet (\S+) --> (\S+)`).FindStringSubmatch(string(detail))
                if len(srcDst) == 3 {
                    gifInterfaces[gifName] = InterfaceConfig{Src: srcDst[1], Dst: srcDst[2], Vlan: ""}
                }
            }
        }
        if strings.Contains(line, "bridge") {
            bridgeName := regexp.MustCompile(`bridge\d+`).FindString(line)
            if bridgeName != "" {
                detail, _ := exec.Command("ifconfig", bridgeName).Output()
                members := regexp.MustCompile(`member: (\S+)`).FindAllStringSubmatch(string(detail), -1)
                memberList := []string{}
                for _, m := range members {
                    memberList = append(memberList, m[1])
                }
                bridgeInterfaces[bridgeName] = BridgeConfig{Members: memberList}
            }
        }
    }
    return gifInterfaces, bridgeInterfaces
}

// loadSettings はローカルの設定ファイルからURLを読み込む
func loadSettings(filename string) (Settings, error) {
    data, err := ioutil.ReadFile(filename)
    if err != nil {
        return Settings{}, fmt.Errorf("failed to read settings file: %v", err)
    }

    var settings Settings
    if err := json.Unmarshal(data, &settings); err != nil {
        return Settings{}, fmt.Errorf("failed to unmarshal settings: %v", err)
    }

    if settings.URL == "" {
        return Settings{}, fmt.Errorf("URL is not specified in settings file")
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
func applyConfig(gifsToAdd, gifsToRemove map[string]InterfaceConfig, bridgesToAdd, bridgesToRemove map[string]BridgeConfig, configs []TunnelConfig) {
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

    // gifインターフェースの追加と設定
    for gif, config := range gifsToAdd {
        if err := runCommand("ifconfig", gif, "create"); err != nil {
            log.Printf("Failed to create gif %s: %v", gif, err)
            continue
        }
        if err := runCommand("ifconfig", gif, "tunnel", config.Src, config.Dst); err != nil {
            log.Printf("Failed to configure tunnel for %s: %v", gif, err)
            continue
        }
        if err := runCommand("ifconfig", gif, "up"); err != nil {
            log.Printf("Failed to bring up gif %s: %v", gif, err)
        }
    }

    // VLANの設定（物理インターフェース名を動的に取得）
    for _, config := range configs {
        vlanIface := fmt.Sprintf("%s.%s", config.PhysicalIface, config.VlanID)
        if err := runCommand("ifconfig", vlanIface, "create"); err != nil {
            log.Printf("Failed to create VLAN %s: %v", vlanIface, err)
            continue
        }
        if err := runCommand("ifconfig", vlanIface, "vlan", config.VlanID, "vlandev", config.PhysicalIface, "up"); err != nil {
            log.Printf("Failed to configure VLAN %s: %v", vlanIface, err)
        }
    }

    // bridgeインターフェースの追加と設定
    for bridge, config := range bridgesToAdd {
        if err := runCommand("ifconfig", bridge, "create"); err != nil {
            log.Printf("Failed to create bridge %s: %v", bridge, err)
            continue
        }
        for _, member := range config.Members {
            if err := runCommand("ifconfig", bridge, "addm", member); err != nil {
                log.Printf("Failed to add member %s to bridge %s: %v", member, bridge, err)
            }
        }
        if err := runCommand("ifconfig", bridge, "up"); err != nil {
            log.Printf("Failed to bring up bridge %s: %v", bridge, err)
        }
    }
}

// calculateDiff は現在の状態とJSONデータの差分を計算
func calculateDiff(currentGifs map[string]InterfaceConfig, currentBridges map[string]BridgeConfig, configs []TunnelConfig) (
    map[string]InterfaceConfig, map[string]InterfaceConfig, map[string]BridgeConfig, map[string]BridgeConfig) {
    jsonGifs := make(map[string]InterfaceConfig)
    jsonBridges := make(map[string]BridgeConfig)

    for _, config := range configs {
        jsonGifs[config.Gif] = InterfaceConfig{Src: config.SrcAddr, Dst: config.DstAddr, Vlan: config.VlanID}
        jsonBridges[config.Bridge] = BridgeConfig{Members: []string{config.Gif, fmt.Sprintf("%s.%s", config.PhysicalIface, config.VlanID)}}
    }

    gifsToAdd := make(map[string]InterfaceConfig)
    gifsToRemove := make(map[string]InterfaceConfig)
    bridgesToAdd := make(map[string]BridgeConfig)
    bridgesToRemove := make(map[string]BridgeConfig)

    for k, v := range jsonGifs {
        if _, exists := currentGifs[k]; !exists {
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
    settingsFile := "settings.json" // ローカル設定ファイル
    interval := 30 * time.Second    // フェッチ間隔

    for {
        // ローカル設定ファイルからURLを読み込み
        settings, err := loadSettings(settingsFile)
        if err != nil {
            log.Printf("Failed to load settings: %v", err)
            time.Sleep(interval)
            continue
        }

        // 現在のインターフェース状態を取得
        currentGifs, currentBridges := getCurrentInterfaces()

        // URLからJSONデータをフェッチ
        configs, err := fetchJSON(settings.URL)
        if err != nil {
            log.Printf("Failed to fetch JSON from %s: %v", settings.URL, err)
            time.Sleep(interval)
            continue
        }

        // 差分を計算し設定を適用
        gifsToAdd, gifsToRemove, bridgesToAdd, bridgesToRemove := calculateDiff(currentGifs, currentBridges, configs)
        applyConfig(gifsToAdd, gifsToRemove, bridgesToAdd, bridgesToRemove, configs)

        log.Printf("Configuration check completed. Sleeping for %v...", interval)
        time.Sleep(interval)
    }
}
