package dlna

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// soapCall 向控制 URL 发起一次 UPnP SOAP 动作调用，返回响应参数名值表。
// args 中的参数值会被 XML 转义，DIDL-Lite 等嵌套 XML 以纯文本嵌入即可。
func soapCall(ctx context.Context, client *http.Client, controlURL, serviceType, action string, args [][2]string) (map[string]string, error) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="utf-8"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"` +
		` s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:` + action +
		` xmlns:u="` + serviceType + `">`)
	for _, kv := range args {
		body.WriteString("<" + kv[0] + ">")
		xml.EscapeText(&body, []byte(kv[1]))
		body.WriteString("</" + kv[0] + ">")
	}
	body.WriteString("</u:" + action + "></s:Body></s:Envelope>")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", fmt.Sprintf(`"%s#%s"`, serviceType, action))

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SOAP %s 请求失败: %w", action, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("SOAP %s 读取响应失败: %w", action, err)
	}
	if resp.StatusCode != http.StatusOK {
		// UPnP 约定失败时以 500 返回 SOAP Fault，尝试解析出错误码。
		if code, desc, perr := parseSOAPFault(bytes.NewReader(data)); perr == nil {
			if code != "" {
				return nil, fmt.Errorf("SOAP %s 失败: HTTP %d, UPnP 错误 %s: %s", action, resp.StatusCode, code, desc)
			}
			return nil, fmt.Errorf("SOAP %s 失败: HTTP %d", action, resp.StatusCode)
		}
		return nil, fmt.Errorf("SOAP %s 失败: HTTP %d", action, resp.StatusCode)
	}
	return parseSOAPResponse(bytes.NewReader(data), action)
}

// parseSOAPResponse 解析 SOAP 响应 Body 中第一个元素的子元素为名值表。
// 厂商实现的命名空间前缀各不相同，这里只按 XML 元素局部名匹配。
func parseSOAPResponse(r io.Reader, action string) (map[string]string, error) {
	decoder := xml.NewDecoder(r)
	want := action + "Response"
	args := map[string]string{}

	var (
		inResponse bool
		curName    string
		curValue   strings.Builder
		depth      int
	)
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析 SOAP 响应失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !inResponse {
				if t.Name.Local == want {
					inResponse = true
					depth = 0
				}
				continue
			}
			depth++
			if depth == 1 {
				curName = t.Name.Local
				curValue.Reset()
			}
		case xml.CharData:
			if inResponse && depth >= 1 {
				curValue.Write(t)
			}
		case xml.EndElement:
			if !inResponse {
				continue
			}
			if depth == 0 {
				if t.Name.Local == want {
					return args, nil
				}
				continue
			}
			depth--
			if depth == 0 && curName != "" {
				args[curName] = curValue.String()
				curName = ""
			}
		}
	}
	if inResponse {
		return args, nil
	}
	return nil, fmt.Errorf("SOAP 响应中缺少 %s 元素", want)
}

// parseSOAPFault 从 SOAP Fault 响应中提取 UPnP 错误码与描述。
// Fault 的层级和命名空间各厂商实现不一，这里按元素局部名扫描。
func parseSOAPFault(r io.Reader) (code, desc string, err error) {
	decoder := xml.NewDecoder(r)
	var inError bool
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "UPnPError":
				inError = true
			case inError && t.Name.Local == "errorCode":
				var s string
				if decoder.DecodeElement(&s, &t) == nil {
					code = s
				}
			case inError && t.Name.Local == "errorDescription":
				var s string
				if decoder.DecodeElement(&s, &t) == nil {
					desc = s
				}
			}
		case xml.EndElement:
			if t.Name.Local == "UPnPError" {
				inError = false
			}
		}
	}
	return code, desc, nil
}
