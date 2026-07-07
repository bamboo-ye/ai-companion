package com.aicompanion.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent { AICompanionApp() }
    }
}

private data class HomeModule(val title: String, val subtitle: String, val color: Color)

@Composable
private fun AICompanionApp() {
    val modules = listOf(
        HomeModule("情感陪伴", "温暖倾听 · 心灵陪伴", Color(0xFFE96D94)),
        HomeModule("生活助手", "日程提醒 · 生活帮手", Color(0xFFE49A2C)),
        HomeModule("工作伙伴", "高效协作 · 智能办公", Color(0xFF587BD2)),
    )

    MaterialTheme {
        LazyColumn(
            modifier = Modifier.fillMaxSize().background(Color(0xFFFFFAF3)),
            contentPadding = PaddingValues(24.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            item {
                Column(modifier = Modifier.padding(vertical = 24.dp)) {
                    Text("伴AI · M0", color = Color(0xFF7667C6), fontWeight = FontWeight.Bold)
                    Text("今天想从哪里开始？", fontSize = 30.sp, fontWeight = FontWeight.Bold)
                    Text("懂陪伴，也能一起把事情做好。", color = Color(0xFF7C6864))
                }
            }
            items(modules) { module -> ModuleCard(module) }
        }
    }
}

@Composable
private fun ModuleCard(module: HomeModule) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        shape = RoundedCornerShape(28.dp),
        colors = CardDefaults.cardColors(containerColor = Color.White),
    ) {
        Column(modifier = Modifier.padding(24.dp)) {
            Text(module.title, color = module.color, fontSize = 24.sp, fontWeight = FontWeight.Bold)
            Text(module.subtitle, modifier = Modifier.padding(top = 8.dp), color = Color(0xFF7C6864))
        }
    }
}

