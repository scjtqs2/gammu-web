package smsd

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
)

// https://zh.wikipedia.org/zh-sg/%E5%9B%BD%E9%99%85%E7%94%B5%E8%AF%9D%E5%8C%BA%E5%8F%B7%E5%88%97%E8%A1%A8

func trimCountryCode(number string) string {
	if len(number) < 3 {
		return number
	}
	code := number[:4]
	if code[:3] == "+86" {
		return number[3:]
	} else if code[:2] == "+1" {
		return number[1:]
	}
	return number
}

func trimPlus(number string) string {
	if len(number) < 3 {
		return number
	}
	if number[0] == '+' {
		return number[1:]
	}
	return number

}

func AddCountryCode(number string) string {
	if number == "" {
		return number
	}

	// 先清理号码格式
	cleaned := cleanPhoneNumber(number)

	// 如果号码已经包含国家代码，直接返回
	if strings.HasPrefix(cleaned, "+") {
		return cleaned
	}

	// 添加国家代码
	if GSM_StateMachine != nil {
		return GSM_StateMachine.country + cleaned
	}

	// 默认中国区号
	return "+86" + cleaned
}

func parseCountry(c string) string {
	if c == "" {
		log.Warn("parseCountry: 收到空字符串")
		return "+86" // 默认中国区号
	}

	// 转换为小写以便不区分大小写匹配
	lowerC := strings.ToLower(c)

	// 中国运营商匹配（中英文）
	chinaKeywords := []string{
		// 英文
		"china", "chinese", "cn", "chinamobile", "china mobile",
		"chinaunicom", "china unicom", "unicom",
		"chinatelecom", "china telecom", "telecom",
		"chinabroadnet", "china broadnet", "broadnet", "cbn",
		"cmcc", "cucc", "ctcc",
		// 中文
		"中国", "移动", "联通", "电信", "广电", "中国移动",
		"中国联通", "中国电信", "中国广电",
	}

	for _, keyword := range chinaKeywords {
		if strings.Contains(lowerC, keyword) {
			return "+86"
		}
	}

	// 美国运营商匹配
	usKeywords := []string{
		"at&t", "t-mobile", "t mobile", "sprint", "verizon",
		"metropcs", "metro pcs", "us", "usa", "united states",
		"america", "american",
	}

	for _, keyword := range usKeywords {
		if strings.Contains(lowerC, keyword) {
			return "+1"
		}
	}

	// 其他常见国家/地区
	countryMappings := map[string]string{
		// 亚洲
		"hong kong": "+852", "香港": "+852",
		"macau": "+853", "澳门": "+853",
		"taiwan": "+886", "台湾": "+886",
		"japan": "+81", "日本": "+81",
		"korea": "+82", "韩国": "+82",
		"singapore": "+65", "新加坡": "+65",
		"malaysia": "+60", "马来西亚": "+60",
		"thailand": "+66", "泰国": "+66",
		"india": "+91", "印度": "+91",

		// 欧洲
		"germany": "+49", "德国": "+49",
		"france": "+33", "法国": "+33",
		"uk": "+44", "united kingdom": "+44", "英国": "+44",
		"italy": "+39", "意大利": "+39",
		"spain": "+34", "西班牙": "+34",
		"russia": "+7", "俄罗斯": "+7",

		// 其他
		"canada": "+1", "加拿大": "+1",
		"australia": "+61", "澳大利亚": "+61",
		"brazil": "+55", "巴西": "+55",
	}

	for keyword, code := range countryMappings {
		if strings.Contains(lowerC, keyword) {
			return code
		}
	}

	// 如果无法识别，记录警告并使用默认值
	log.Warnf("无法识别运营商/国家: %s，使用默认值 +86", c)
	return "+86" // 默认中国区号
}

// 验证手机号码格式
func ValidatePhoneNumber(number string) bool {
	if number == "" {
		return false
	}

	// 基本格式检查：以+开头，后面跟数字
	if !strings.HasPrefix(number, "+") {
		return false
	}

	// 检查除+号外是否全是数字
	for i, ch := range number {
		if i == 0 {
			continue // 跳过+号
		}
		if ch < '0' || ch > '9' {
			return false
		}
	}

	// 检查长度（国家代码+号码，通常至少8位，最多15位）
	if len(number) < 8 || len(number) > 16 {
		return false
	}

	return true
}

// 标准化手机号码（确保格式统一）
func NormalizePhoneNumber(number string) (string, error) {
	cleaned := cleanPhoneNumber(number)

	if !ValidatePhoneNumber(cleaned) {
		return "", fmt.Errorf("无效的手机号码格式: %s", number)
	}

	return cleaned, nil
}
