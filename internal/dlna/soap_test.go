package dlna

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSOAPCallRoundTrip(t *testing.T) {
	var gotAction, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAction = r.Header.Get("SOAPACTION")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>` +
			`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<s:Body><u:PlayResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"></u:PlayResponse></s:Body>` +
			`</s:Envelope>`))
	}))
	defer srv.Close()

	out, err := soapCall(context.Background(), srv.Client(), srv.URL, AVTransportServiceType, "Play",
		[][2]string{{"Speed", "1"}})
	if err != nil {
		t.Fatalf("soapCall 返回错误: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("期望空参数表，得到 %v", out)
	}
	if want := `"urn:schemas-upnp-org:service:AVTransport:1#Play"`; gotAction != want {
		t.Errorf("SOAPACTION = %q, 期望 %q", gotAction, want)
	}
	for _, want := range []string{
		`<u:Play xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">`,
		`<Speed>1</Speed>`,
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("请求体缺少 %s，实际: %s", want, gotBody)
		}
	}
}

func TestAVTransportInjectsInstanceID(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<s:Body><u:PlayResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"></u:PlayResponse></s:Body>` +
			`</s:Envelope>`))
	}))
	defer srv.Close()

	dev := &Device{
		FriendlyName: "测试电视",
		Services:     []Service{{Type: AVTransportServiceType, ControlURL: srv.URL}},
	}
	if err := NewRenderer(dev).Play(testCtx()); err != nil {
		t.Fatalf("Play 失败: %v", err)
	}
	if !strings.Contains(gotBody, "<InstanceID>0</InstanceID>") {
		t.Errorf("AVTransport 动作应自动携带 InstanceID=0，实际: %s", gotBody)
	}
}

func TestSOAPCallParsesArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?>` +
			`<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<SOAP-ENV:Body><u:GetPositionInfoResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">` +
			`<Track>1</Track><TrackDuration>1:01:05</TrackDuration><RelTime>0:10:20</RelTime>` +
			`</u:GetPositionInfoResponse></SOAP-ENV:Body></SOAP-ENV:Envelope>`))
	}))
	defer srv.Close()

	out, err := soapCall(context.Background(), srv.Client(), srv.URL, AVTransportServiceType, "GetPositionInfo",
		[][2]string{{"InstanceID", "0"}})
	if err != nil {
		t.Fatalf("soapCall 返回错误: %v", err)
	}
	if out["TrackDuration"] != "1:01:05" || out["RelTime"] != "0:10:20" {
		t.Fatalf("参数解析错误: %v", out)
	}
}

func TestSOAPCallFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<?xml version="1.0"?>` +
			`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<s:Body><s:Fault><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">` +
			`<errorCode>714</errorCode><errorDescription>Illegal Argument</errorDescription>` +
			`</UPnPError></detail></s:Fault></s:Body></s:Envelope>`))
	}))
	defer srv.Close()

	_, err := soapCall(context.Background(), srv.Client(), srv.URL, AVTransportServiceType, "Seek", nil)
	if err == nil || !strings.Contains(err.Error(), "714") {
		t.Fatalf("期望包含 UPnP 错误码的失败，得到: %v", err)
	}
}

func TestSOAPCallEscapesArgumentValues(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<s:Body><u:SetAVTransportURIResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"></u:SetAVTransportURIResponse></s:Body>` +
			`</s:Envelope>`))
	}))
	defer srv.Close()

	_, err := soapCall(context.Background(), srv.Client(), srv.URL, AVTransportServiceType, "SetAVTransportURI",
		[][2]string{{"CurrentURI", "http://x/f?a=1&b=2"}})
	if err != nil {
		t.Fatalf("soapCall 返回错误: %v", err)
	}
	if !strings.Contains(gotBody, "http://x/f?a=1&amp;b=2") {
		t.Errorf("参数值未被 XML 转义: %s", gotBody)
	}
}
