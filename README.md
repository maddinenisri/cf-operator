# Cloudformation Kubernetes Operator 

## Project Creation
```sh
  mkdir cf-operator && cd cf-operator
  operator-sdk init --domain=mdstechinc.com --repo=github.com/maddinenisri/cf-operator
```

## Add API endpoint
```sh
  operator-sdk create api --group=infra --version=v1alpha1 --kind=CloudFormation
```

## After defining API Contract generate config
```sh
  make manifests
```