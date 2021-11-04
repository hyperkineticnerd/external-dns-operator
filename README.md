# ExternalDNS Operator

The `ExternalDNS` Operator allows you to deploy and manage [ExternalDNS](https://github.com/kubernetes-sigs/external-dns), a cluster-internal component which makes Kubernetes resources discoverable through public DNS servers. \
**Note**: This Operator is in the early stages of implementation. For more information, see
[ExternalDNS Operator OpenShift Enhancement Proposal](https://github.com/openshift/enhancements/pull/786).

## Deploying the `ExternalDNS` Operator
The following procedure describes how to deploy the `ExternalDNS` Operator for AWS.

### Installing the `ExternalDNS` Operator by building and pushing the Operator image to a registry
1. Build and push the Operator image to a registry:
   ```sh
   $ export IMG=<registry>/<username>/external-dns-operator:latest
   $ make image-build
   $ make image-push
   ```
2. Run the following command to deploy the `ExternalDNS` Operator:
    ```
    $ make deploy
    ```
3. The previous step deploys the validation webhook, which requires TLS authentication for the webhook server. The
   manifests deployed through the `make deploy` command do not contain a valid certificate and key. You must provision a valid certificate and key through other tools.
   You can use a convenience script, `hack/generate-certs.sh` to generate the certificate bundle and patch the validation webhook config.   
   _Important_: Do not use the hack/generate-certs.sh script in a production environment.   
   Run the `hack/generate-certs.sh` script with the following inputs:
   ```bash
   $ hack/generate-certs.sh --service webhook-service --webhook validating-webhook-configuration \
   --secret webhook-server-cert --namespace external-dns-operator
   ```
4. Now you can deploy an instance of ExternalDNS:
    * Run the following command to create the credentials secret for AWS:
        ```bash
        $ kubectl -n external-dns-operator create secret generic aws-access-key \
                --from-literal=aws_access_key_id=${ACCESS_KEY_ID} \
                --from-literal=aws_secret_access_key=${ACCESS_SECRET_KEY}
        ```
       *Note*: See [this guide](./docs/usage.md) for instructions specific to other providers.
      
    * Run the following command:
      ```bash
      # for AWS
      $ kubectl apply -k config/samples/aws`
      ```
       *Note*: For other providers, see `config/samples/`.


### Installing the `ExternalDNS` Operator using a custom index image on OperatorHub
**Note**: By default container engine used is docker but you can specify podman by adding CONTAINER_ENGINE=podman to your image build and push commands as mentioned below.
    
1. Build and push operator image to registry.
   
a. Select the container runtime you want. Either podman or docker. 
   ```sh
   export CONTAINER_ENGINE=podman
   ```
   b. Set your image name 
   ```sh
   $ export IMG=<registry>/<username>/external-dns-operator:latest
   ```
   c. Build and push the image
```sh
   make image-build
   make image-push
```
   
2. Build the bundle image to a registry:
  
  a. Export your expected image name which shall have your registry name in an env var.
   ```sh
   $ export BUNDLE_IMG=<registry>/<username>/external-dns-operator-bundle:latest
   ```
   
  b. In the bundle/manifests/external-dns-operator_clusterserviceversion.yaml
     add the operator image created in Step 1 as follows - 
   ```sh
    In annotations:
    Change containerImage: quay.io/openshift/origin-external-dns-operator:latest
    to containerImage: <registry>/<username, repository name>/external-dns-operator:latest
   
    In spec:
    Change image: quay.io/openshift/origin-external-dns-operator:latest
    to image: <registry>/<username, repository name>/external-dns-operator:latest
  ```
  
  c. Build the image
   ```sh   
   $ make bundle-image-build
   ```
   
3. Push the image.
  ```sh
   $ make bundle-image-push
   ```

4. Build and push the image index for `operator-registry`:
   ```sh
   $ export INDEX_IMG=<registry>/<username>/external-dns-operator-bundle-index:1.0.0
   $ make index-image-build
   $ make index-image-push
   ```

5. Create the `external-dns-operator` namespace:
   ```sh
   $ oc create ns external-dns-operator
   ```

6. You may need to link the registry secret to the pod of `external-dns-operator` created in the `openshift-marketplace` namespace if the image is not made public ([Doc link](https://docs.openshift.com/container-platform/4.9/openshift_images/managing_images/using-image-pull-secrets.html#images-allow-pods-to-reference-images-from-secure-registries_using-image-pull-secrets). If you are using podman then these are the instructions:

Login to your registry
   ```sh
      $ podman login quay.io
Authenticating with existing credentials...
Existing credentials are valid. Already logged in to quay.io
   ```

Create a secret with registry auth details
```sh
$ oc -n openshift-marketplace create secret generic extdns-olm-secret  --type=kubernetes.io/dockercfg  --from-file=.dockercfg=${XDG_RUNTIME_DIR}/containers/auth.json
```

Link the secret to default and builder service accounts
```sh
$ oc secrets link builder extdns-olm-secret -n openshift-marketplace
$ oc secrets link default extdns-olm-secret --for=pull -n openshift-marketplace
````

*Note* the secret to the pod of `external-dns-operator` is part of the bundle created in step 1.


7. Create the `Catalogsource` object 
   
   ```yaml
   $ cat <<EOF | oc apply -f -
   apiVersion: operators.coreos.com/v1alpha1
   kind: CatalogSource
   metadata:
     name: external-dns-operator
     namespace: openshift-marketplace
   spec:
     sourceType: grpc
     image: <registry>/<username>/external-dns-operator-bundle-index:1.0.0
   EOF
   ```

8. Now go to the web console of OCP, then select Operator -> Operator Hub.
   Type external-dns-operator in search, select and install it in external-dns-operator namespace.
   
**Note**: Subscription object is created in subsequent steps when you install via web console.
       So ignore step 9 if you are installing operator via web console.

9. Create a subscription object to install the Operator:
   
    ```yaml
    cat <<EOF | oc apply -f -
    apiVersion: operators.coreos.com/v1alpha1
    kind: Subscription
    metadata:
      name: external-dns-operator
      namespace: external-dns-operator
    spec:
      channel: alpha
      name: external-dns-operator
      source: external-dns-operator
      sourceNamespace: openshift-marketplace
    EOF
    ```
    **Note**: You can install the `ExternalDNS` Operator through the web console: Navigate to  `Operators` -> `OperatorHub`, search for the `ExternalDNS operator`,  and install the operator.
