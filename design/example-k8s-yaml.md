# deploy/k8s/deployment.yaml
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: auth-manager
  namespace: terminal
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: auth-manager
  namespace: terminal
spec:
  replicas: 1
  selector:
    matchLabels:
      app: auth-manager
  template:
    metadata:
      labels:
        app: auth-manager
    spec:
      serviceAccountName: auth-manager
      automountServiceAccountToken: false
      securityContext:
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: auth-manager
          image: auth-manager:latest
          imagePullPolicy: Never
          env:
            - name: PORT
              value: "8888"
            - name: FIREBASE_PROJECT_ID
              value: "xxxxxxz"
            - name: FIREBASE_CREDENTIALS_FILE
              value: "/config/service-account.json"
            - name: CORS_ALLOWED_ORIGINS
              value: "https://xxxxxxzzxx"
            - name: ALLOW_LOCAL_DEV_ORIGINS
              value: "false"
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
            runAsUser: 10001
            runAsGroup: 10001
            capabilities:
              drop:
                - ALL
          ports:
            - containerPort: 8888
          volumeMounts:
            - name: firebase-secret
              mountPath: "/config"
              readOnly: true
      volumes:
        - name: firebase-secret
          secret:
            secretName: firebase-service-account
            optional: false
---
apiVersion: v1
kind: Service
metadata:
  name: auth-manager
  namespace: terminal
spec:
  type: ClusterIP
  selector:
    app: auth-manager
  ports:
    - protocol: TCP
      port: 8888
      targetPort: 8888
```
