package com.wails.app;

import android.app.Instrumentation;
import android.webkit.ConsoleMessage;
import android.webkit.JavascriptInterface;
import android.webkit.WebChromeClient;
import android.webkit.WebView;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.BufferedReader;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

@RunWith(AndroidJUnit4.class)
public class MainActivityTest {
    @Test
    public void localDesktopWebSocketsUseApplicationSettings() throws Exception {
        checkLocalWebSockets();
    }

    private void checkLocalWebSockets() throws Exception {
        Instrumentation instrumentation = InstrumentationRegistry.getInstrumentation();
        List<String> console = Collections.synchronizedList(new ArrayList<>());
        AtomicReference<String> result = new AtomicReference<>("no JavaScript result");
        CountDownLatch done = new CountDownLatch(1);
        WebView[] view = new WebView[1];
        try (ServerSocket server = new ServerSocket(0, 3, InetAddress.getByName("127.0.0.1"))) {
            server.setSoTimeout(20000);
            Thread echo = new Thread(() -> {
                try {
                    for (int i = 0; i < 3; i++) {
                        try (Socket socket = server.accept()) {
                            socket.setSoTimeout(10000);
                            echoWebSocket(socket);
                        }
                    }
                } catch (Exception error) {
                    console.add("local server: " + error);
                }
            });
            echo.setDaemon(true);
            echo.start();
            String endpoint = "ws://127.0.0.1:" + server.getLocalPort();
            String html = "<script>"
                    + "(async()=>{for(const path of ['/rdp-token','/','/spice-token']){"
                    + "await new Promise((resolve,reject)=>{"
                    + "const ws=new WebSocket('" + endpoint + "'+path);ws.binaryType='arraybuffer';"
                    + "ws.onopen=()=>ws.send(new Uint8Array([0,127,128,255]));"
                    + "ws.onerror=()=>reject(new Error(path+': socket error, state='+ws.readyState));"
                    + "ws.onmessage=ev=>{const b=new Uint8Array(ev.data);"
                    + "if(b.length===4&&b[0]===0&&b[1]===127&&b[2]===128&&b[3]===255){resolve();}"
                    + "else reject(new Error(path+': invalid binary echo'));ws.close();};"
                    + "});}probe.done('ok');})().catch(error=>probe.done(String(error)));"
                    + "</script>";
            instrumentation.runOnMainSync(() -> {
                view[0] = new WebView(instrumentation.getTargetContext());
                MainActivity.configureWebViewSettings(view[0].getSettings());
                view[0].setWebChromeClient(new WebChromeClient() {
                    @Override
                    public boolean onConsoleMessage(ConsoleMessage message) {
                        console.add(message.message());
                        return true;
                    }
                });
                view[0].addJavascriptInterface(new Object() {
                    @JavascriptInterface
                    public void done(String value) {
                        result.set(value);
                        done.countDown();
                    }
                }, "probe");
                view[0].loadDataWithBaseURL("https://wails.localhost/", html, "text/html", "UTF-8", null);
            });
            boolean completed = done.await(25, TimeUnit.SECONDS);
            assertTrue("WebSocket test timed out: " + console, completed);
            assertEquals("WebView messages: " + console, "ok", result.get());
        } finally {
            instrumentation.runOnMainSync(() -> {
                if (view[0] != null) view[0].destroy();
            });
        }
    }

    private static void echoWebSocket(Socket socket) throws Exception {
        InputStream input = socket.getInputStream();
        BufferedReader headers = new BufferedReader(new InputStreamReader(input, StandardCharsets.US_ASCII));
        String key = null;
        String line;
        while ((line = headers.readLine()) != null && !line.isEmpty()) {
            if (line.toLowerCase(java.util.Locale.ROOT).startsWith("sec-websocket-key:")) {
                key = line.substring(line.indexOf(':') + 1).trim();
            }
        }
        if (key == null) throw new IllegalStateException("missing WebSocket handshake key");
        byte[] hash = MessageDigest.getInstance("SHA-1")
                .digest((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").getBytes(StandardCharsets.US_ASCII));
        String accept = android.util.Base64.encodeToString(hash, android.util.Base64.NO_WRAP);
        OutputStream output = socket.getOutputStream();
        output.write(("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n"
                + "Connection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
                .getBytes(StandardCharsets.US_ASCII));
        output.flush();
        int opcode = input.read();
        int length = input.read();
        if (opcode != 0x82 || length != 0x84) throw new IllegalStateException("unexpected binary frame");
        byte[] mask = new byte[4];
        byte[] payload = new byte[4];
        readFully(input, mask);
        readFully(input, payload);
        for (int i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
        output.write(new byte[]{(byte) 0x82, 4});
        output.write(payload);
        output.flush();
    }

    private static void readFully(InputStream input, byte[] buffer) throws Exception {
        int offset = 0;
        while (offset < buffer.length) {
            int count = input.read(buffer, offset, buffer.length - offset);
            if (count < 0) throw new IllegalStateException("truncated WebSocket frame");
            offset += count;
        }
    }
}
