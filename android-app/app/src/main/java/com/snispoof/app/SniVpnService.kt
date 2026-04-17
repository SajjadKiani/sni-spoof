package com.snispoof.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.IBinder
import go.sni_spoof.Snispoof

class SniVpnService : VpnService() {
    private var running = false

    override fun onBind(intent: Intent?): IBinder? {
        return super.onBind(intent)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> stopVpn()
            ACTION_START -> {
                val config = intent.getStringExtra(EXTRA_CONFIG_JSON) ?: ""
                startVpn(config)
            }
        }
        return Service.START_STICKY
    }

    private fun startVpn(configJson: String) {
        if (running) {
            return
        }

        createNotificationChannel()
        startForeground(NOTIFICATION_ID, buildNotification("Starting VPN engine"))

        try {
            val builder = Builder()
                .setSession("SniSpoof")
                .setMtu(1500)
                .addAddress("10.9.0.2", 32)
                .addRoute("0.0.0.0", 0)
                .addDnsServer("1.1.1.1")

            val pfd = builder.establish() ?: throw IllegalStateException("failed to establish VPN TUN")
            val tunFd = pfd.detachFd()

            Snispoof.start(tunFd, configJson)
            running = true
            val status = Snispoof.status()
            startForeground(NOTIFICATION_ID, buildNotification("VPN active: $status"))
        } catch (e: Exception) {
            running = false
            startForeground(NOTIFICATION_ID, buildNotification("VPN error: ${e.message}"))
            stopSelf()
        }
    }

    private fun stopVpn() {
        if (running) {
            try {
                Snispoof.stop()
            } catch (_: Exception) {
            }
        }
        running = false
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    override fun onDestroy() {
        stopVpn()
        super.onDestroy()
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val manager = getSystemService(NotificationManager::class.java)
            val channel = NotificationChannel(
                CHANNEL_ID,
                "SniSpoof VPN",
                NotificationManager.IMPORTANCE_LOW
            )
            manager.createNotificationChannel(channel)
        }
    }

    private fun buildNotification(content: String): Notification {
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            Notification.Builder(this)
        }

        return builder
            .setContentTitle("SniSpoof")
            .setContentText(content)
            .setSmallIcon(android.R.drawable.stat_sys_warning)
            .setOngoing(true)
            .build()
    }

    companion object {
        const val ACTION_START = "com.snispoof.app.action.START"
        const val ACTION_STOP = "com.snispoof.app.action.STOP"
        const val EXTRA_CONFIG_JSON = "config_json"

        private const val CHANNEL_ID = "snispoof_vpn"
        private const val NOTIFICATION_ID = 1001
    }
}
