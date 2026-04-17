package com.snispoof.app

import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import go.sni_spoof.Snispoof
import org.json.JSONObject

class MainActivity : AppCompatActivity() {
    private lateinit var listenPort: EditText
    private lateinit var connectIp: EditText
    private lateinit var connectPort: EditText
    private lateinit var fakeSni: EditText
    private lateinit var toggleButton: Button
    private lateinit var statusText: TextView

    private val handler = Handler(Looper.getMainLooper())
    private val statusPoller = object : Runnable {
        override fun run() {
            val status = Snispoof.status()
            statusText.text = getString(R.string.status_format, status)
            toggleButton.text = if (status == "running") getString(R.string.stop_vpn) else getString(R.string.start_vpn)
            handler.postDelayed(this, 750)
        }
    }

    private val vpnPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) {
        if (VpnService.prepare(this) == null) {
            startVpnService()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        listenPort = findViewById(R.id.listenPortInput)
        connectIp = findViewById(R.id.connectIpInput)
        connectPort = findViewById(R.id.connectPortInput)
        fakeSni = findViewById(R.id.fakeSniInput)
        toggleButton = findViewById(R.id.toggleButton)
        statusText = findViewById(R.id.statusText)

        listenPort.setText("40443")
        connectIp.setText("104.18.4.130")
        connectPort.setText("443")
        fakeSni.setText("security.vercel.com")

        toggleButton.setOnClickListener {
            val status = Snispoof.status()
            if (status == "running") {
                stopVpnService()
            } else {
                requestVpnPermissionAndStart()
            }
        }
    }

    override fun onStart() {
        super.onStart()
        handler.post(statusPoller)
    }

    override fun onStop() {
        handler.removeCallbacks(statusPoller)
        super.onStop()
    }

    private fun requestVpnPermissionAndStart() {
        val intent = VpnService.prepare(this)
        if (intent != null) {
            vpnPermissionLauncher.launch(intent)
        } else {
            startVpnService()
        }
    }

    private fun startVpnService() {
        val configJson = JSONObject()
            .put("LISTEN_HOST", "127.0.0.1")
            .put("LISTEN_PORT", listenPort.text.toString().toInt())
            .put("CONNECT_IP", connectIp.text.toString().trim())
            .put("CONNECT_PORT", connectPort.text.toString().toInt())
            .put("FAKE_SNI", fakeSni.text.toString().trim())
            .toString()

        val intent = Intent(this, SniVpnService::class.java).apply {
            action = SniVpnService.ACTION_START
            putExtra(SniVpnService.EXTRA_CONFIG_JSON, configJson)
        }

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent)
        } else {
            startService(intent)
        }
    }

    private fun stopVpnService() {
        val intent = Intent(this, SniVpnService::class.java).apply {
            action = SniVpnService.ACTION_STOP
        }
        startService(intent)
    }
}
