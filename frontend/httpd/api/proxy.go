package api

import (
	"errors"
	"fmt"
	"github.com/HouzuoGuo/websh/frontend/common"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
)

const ProxyInjectJS = `
<script type="text/javascript">
websh_proxy_scheme_host = '%s';
websh_proxy_scheme_host_slash = websh_proxy_scheme_host + '/';
websh_proxy_scheme_host_handle = '%s';
websh_proxy_scheme_host_handle_param = websh_proxy_scheme_host_handle + '?u=';
websh_browse_scheme_host = '%s';
websh_browse_scheme_host_path = '%s';

function websh_rewrite_url(before) {
    var after;
    if (before == '' || before == '#' || before.indexOf('data') == 0 || before.indexOf('javascript') == 0 || before.indexOf(websh_proxy_scheme_host_handle_param) == 0) {
        after = before;
    } else if (before.indexOf(websh_proxy_scheme_host_slash) == 0) {
        after = websh_proxy_scheme_host_handle_param + encodeURIComponent(websh_browse_scheme_host + '/' + before.substr(websh_proxy_scheme_host_slash.length));
    } else if (before.indexOf('http') == 0) {
        after = websh_proxy_scheme_host_handle_param + encodeURIComponent(before);
    } else if (before.indexOf('../') == 0) {
        after = websh_proxy_scheme_host_handle_param + encodeURIComponent(websh_browse_scheme_host_path + '/' + before);
    } else if (before.indexOf('/') == 0) {
        after = websh_proxy_scheme_host_handle_param + encodeURIComponent(websh_browse_scheme_host + before);
    } else {
        after = websh_proxy_scheme_host_handle_param + encodeURIComponent(websh_browse_scheme_host + '/' + before);
    }
    console.log('before ' + before + ' after ' + after);
    return after;
}

var websh_proxied_ajax_open = window.XMLHttpRequest.prototype.open;
window.XMLHttpRequest.prototype.open = function() {
    var before = arguments[1];
    var after = websh_rewrite_url(before);
    arguments[1] = after;
    return websh_proxied_ajax_open.apply(this, [].slice.call(arguments));
};

function websh_replace_url(elem, attr) {
    var elems = document.getElementsByTagName(elem);
    for (var i = 0; i < elems.length; i++) {
        var before = elems[i][attr];
        if (before != '') {
            elems[i][attr] = websh_rewrite_url(before);
        }
    }
}

function websh_replace_few() {
    websh_replace_url('a', 'href');
    websh_replace_url('img', 'src');
    websh_replace_url('form', 'action');
}

function websh_replace_many() {
    websh_replace_few();
    websh_replace_url('link', 'href');
    websh_replace_url('iframe', 'src');

    var script_srcs = [];
    var scripts = document.getElementsByTagName('script');
    for (var i = 0; i < scripts.length; i++) {
        var before = scripts[i]['src'];
        if (before != '') {
            script_srcs.push(websh_rewrite_url(before));
        }
    }
    for (var i = 0; i < script_srcs.length; i++) {
        document.body.appendChild(document.createElement('script')).src=script_srcs[i];
    }
    if (!document.getElementById('websh_replace_few')) {
        var btn = document.createElement('button');
        btn.id = 'websh_replace_few';
        btn.style.cssText = 'font-size: 9px !important; position: fixed !important; bottom: 0px !important; left: 100px !important; zIndex: 999999 !important';
        btn.onclick = websh_replace_few;
        btn.appendChild(document.createTextNode('XY'));
        document.body.appendChild(btn);
    }
    if (!document.getElementById('websh_replace_many')) {
        var btn = document.createElement('button');
        btn.id = 'websh_replace_many';
        btn.style.cssText = 'font-size: 9px !important; position: fixed !important; bottom: 0px !important; left: 200px !important; zIndex: 999999 !important';
        btn.onclick = websh_replace_many;
        btn.appendChild(document.createTextNode('XY-ALL'));
        document.body.appendChild(btn);
    }
}

window.onload = function() {
    websh_replace_many();
};
</script>
` // Snippet of Javascript that has to be injected into proxied web page

// Implement handler for sending Howard an email. The text on the page is deliberately written in Chinese.
type HandleWebProxy struct {
	MyEndpoint string `json:"-"` // URL endpoint to the proxy itself, including prefix /.
}

func (xy *HandleWebProxy) MakeHandler(_ *common.CommandProcessor) (http.HandlerFunc, error) {
	if xy.MyEndpoint == "" {
		return nil, errors.New("MyEndpoint is empty")
	}
	var RemoveRequestHeaders = []string{"Host", "Content-Length", "Accept-Encoding", "Content-Security-Policy", "Set-Cookie"}
	var RemoveResponseHeaders = []string{"Host", "Content-Length", "Transfer-Encoding", "Content-Security-Policy", "Set-Cookie"}

	fun := func(w http.ResponseWriter, r *http.Request) {
		// Figure out where proxy endpoint is located
		proxySchemeHost := r.Host
		if r.TLS == nil {
			proxySchemeHost = "http://" + proxySchemeHost
		} else {
			proxySchemeHost = "https://" + proxySchemeHost
		}
		proxyHandlePath := proxySchemeHost + xy.MyEndpoint
		// Figure out where user wants to go
		browseURL := r.FormValue("u")
		if browseURL == "" {
			return
		}
		fmt.Println("Going to fetch", browseURL)
		fmt.Println("proxySchemeHost", proxySchemeHost)
		fmt.Println("proxyHandlePath", proxyHandlePath)
		urlParts, err := url.Parse(browseURL)
		if err != nil {
			fmt.Println(err)
			return
		}

		browseSchemeHost := fmt.Sprintf("%s://%s", urlParts.Scheme, urlParts.Host)
		browseSchemeHostPath := fmt.Sprintf("%s://%s%s", urlParts.Scheme, urlParts.Host, urlParts.Path)
		browseSchemeHostPathQuery := browseSchemeHostPath
		if urlParts.RawQuery != "" {
			browseSchemeHostPathQuery += "?" + urlParts.RawQuery
		}
		fmt.Println("browseSchemeHost", browseSchemeHost)
		fmt.Println("browseSchemeHostPath", browseSchemeHostPath)
		fmt.Println("browseSchemeHostPathQuery", browseSchemeHostPathQuery)

		myReq, err := http.NewRequest(r.Method, browseSchemeHostPath, r.Body)
		if err != nil {
			fmt.Println("Failed to make proxy request", err)
			return
		}
		// Remove request headers that are not necessary
		myReq.Header = r.Header
		for _, name := range RemoveRequestHeaders {
			myReq.Header.Del(name)
		}
		// Retrieve resource from remote
		client := http.Client{}
		remoteResp, err := client.Do(myReq)
		remoteRespBody, err := ioutil.ReadAll(remoteResp.Body)
		if err != nil {
			fmt.Println("Failed to read response body", err)
			return
		}
		// Copy headers from remote response
		for name, values := range remoteResp.Header {
			w.Header().Set(name, values[0])
			for _, val := range values[1:] {
				w.Header().Add(name, val)
			}
		}
		for _, name := range RemoveResponseHeaders {
			w.Header().Del(name)
		}
		// Just in case they become useful later on
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, PUT, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Type, Authorization")
		// Rewrite HTML response to insert javascript
		w.WriteHeader(remoteResp.StatusCode)
		if strings.HasPrefix(remoteResp.Header.Get("Content-Type"), "text/html") {
			injectedJS := fmt.Sprintf(ProxyInjectJS, proxySchemeHost, proxyHandlePath, browseSchemeHost, browseSchemeHostPath)
			strBody := string(remoteRespBody)
			headIndex := strings.Index(strBody, "<head>")
			if headIndex == -1 {
				bodyIndex := strings.Index(strBody, "<body")
				if bodyIndex == -1 {
					fmt.Println("Will not modify the page")
				} else {
					beforeBody := strBody[0 : bodyIndex-5]
					atAndAfterBody := strBody[bodyIndex:]
					strBody = fmt.Sprintf("%s<head>%s</head>%s", beforeBody, injectedJS, atAndAfterBody)
				}
			} else {
				strBody = strBody[0:headIndex+6] + injectedJS + strBody[headIndex+7:]
			}
			fmt.Println("Send modified page")
			w.Write([]byte(strBody))
		} else {
			fmt.Println("Send original", w.Header().Get("Content-Type"))
			w.Write(remoteRespBody)
		}
	}
	return fun, nil
}
