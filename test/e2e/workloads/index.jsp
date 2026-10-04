<%@ page language="java" contentType="text/html" %>
<%@ page import="java.net.InetAddress" %>
<%@ page import="java.time.LocalDateTime" %>
<%@ page import="java.time.format.DateTimeFormatter" %>
<%
    String hostname = InetAddress.getLocalHost().getHostName();
    String ipAddress = InetAddress.getLocalHost().getHostAddress();
    String timestamp = LocalDateTime.now().format(DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm:ss"));
    long uptimeMillis = java.lang.management.ManagementFactory.getRuntimeMXBean().getUptime();
    long uptimeSeconds = uptimeMillis / 1000;
    long uptimeMinutes = uptimeSeconds / 60;
    long uptimeHours = uptimeMinutes / 60;
%>
<html>
<head>
    <title>CCattler Java App</title>
    <style>
        body { font-family: sans-serif; margin: 40px; background: #f5f5f5; }
        h1 { color: #2c3e50; }
        .info { background: white; padding: 15px 20px; border-radius: 8px; margin: 10px 0; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
        .label { font-weight: bold; color: #555; min-width: 140px; display: inline-block; }
        .value { color: #2c3e50; font-family: monospace; }
        .footer { margin-top: 30px; color: #888; font-size: 0.85em; }
    </style>
</head>
<body>
    <h1>CCattler Java App</h1>
    <div class="info">
        <span class="label">Container ID:</span>
        <span class="value"><%= hostname %></span>
    </div>
    <div class="info">
        <span class="label">Container IP:</span>
        <span class="value"><%= ipAddress %></span>
    </div>
    <div class="info">
        <span class="label">Runtime:</span>
        <span class="value">Apache Tomcat 11 / JRE 21</span>
    </div>
    <div class="info">
        <span class="label">Server Time:</span>
        <span class="value"><%= timestamp %></span>
    </div>
    <div class="info">
        <span class="label">JVM Uptime:</span>
        <span class="value"><%= uptimeHours %>h <%= uptimeMinutes % 60 %>m <%= uptimeSeconds % 60 %>s</span>
    </div>
    <div class="info">
        <span class="label">Managed by:</span>
        <span class="value">CCattler Container Orchestrator</span>
    </div>
    <p class="footer">Refresh the page to see round-robin load balancing across instances.</p>
</body>
</html>
