module github.com/invidian/metallb-hcloud-controller

go 1.15

require (
	github.com/go-logr/logr v0.2.1 // indirect
	github.com/google/go-cmp v0.5.2 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/googleapis/gnostic v0.5.3 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/hetznercloud/hcloud-go v1.22.0
	github.com/imdario/mergo v0.3.11 // indirect
	github.com/prometheus/client_golang v1.8.0
	golang.org/x/crypto v0.0.0-20201016220609-9e8e0b390897 // indirect
	golang.org/x/net v0.0.0-20201026091529-146b70c837a4 // indirect
	golang.org/x/oauth2 v0.0.0-20200902213428-5d25da1a8d43 // indirect
	golang.org/x/sys v0.0.0-20201026173827-119d4633e4d1 // indirect
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e // indirect
	google.golang.org/appengine v1.6.7 // indirect
	k8s.io/api v0.19.3
	k8s.io/apimachinery v0.19.3
	k8s.io/client-go v11.0.0+incompatible
	k8s.io/klog/v2 v2.3.0
	k8s.io/utils v0.0.0-20201015054608-420da100c033 // indirect
)

replace k8s.io/client-go => k8s.io/client-go v0.19.3
