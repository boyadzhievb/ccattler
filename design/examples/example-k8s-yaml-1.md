apiVersion: apps/v1
kind: Deployment
metadata:
  name: maven-deployment
spec:
  replicas: 1
  selector:
    matchLabels:
      app: maven
  template:
    metadata:
      labels:
        app: maven
    spec:
      containers:
      - name: maven-container
        image: mavenimage
        ports:
        - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: maven-service
spec:
  selector:
    app: maven
  ports:
    - protocol: TCP
      port: 80
      targetPort: 8080
  type: NodePort