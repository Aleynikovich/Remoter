package io.github.aleynikovich.remoter

import android.content.Context
import android.content.SharedPreferences
import org.json.JSONArray

/** Saved settings and computers. */
class Prefs(context: Context) {
    private val p: SharedPreferences = context.getSharedPreferences("remoter", Context.MODE_PRIVATE)

    /** Computer addresses (host:port), most recently used first. */
    var computers: List<String>
        get() {
            val raw = p.getString("computers", null) ?: return listOf(DEFAULT_COMPUTER)
            val a = JSONArray(raw)
            return List(a.length()) { a.getString(it) }
        }
        set(v) = p.edit().putString("computers", JSONArray(v.distinct()).toString()).apply()

    fun password(computer: String): String = p.getString("password:$computer", DEFAULT_PASSWORD)!!

    fun setPassword(computer: String, password: String) = p.edit().putString("password:$computer", password).apply()

    fun used(computer: String) {
        computers = listOf(computer) + computers.filter { it != computer }
    }

    var trackpad: Boolean
        get() = p.getBoolean("trackpad", false)
        set(v) = p.edit().putBoolean("trackpad", v).apply()

    var video: Boolean
        get() = p.getBoolean("video", true)
        set(v) = p.edit().putBoolean("video", v).apply()

    var keysBar: Boolean
        get() = p.getBoolean("keysBar", true)
        set(v) = p.edit().putBoolean("keysBar", v).apply()

    var toolbar: Boolean
        get() = p.getBoolean("toolbar", true)
        set(v) = p.edit().putBoolean("toolbar", v).apply()

    companion object {
        const val DEFAULT_COMPUTER = "100.102.208.35:8080"
        const val DEFAULT_PASSWORD = "root"
    }
}
