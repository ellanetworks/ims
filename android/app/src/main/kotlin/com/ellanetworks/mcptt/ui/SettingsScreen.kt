package com.ellanetworks.mcptt.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectVerticalDragGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material.icons.rounded.Add
import androidx.compose.material.icons.rounded.Delete
import androidx.compose.material.icons.rounded.DragHandle
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.zIndex
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.ellanetworks.mcptt.BuildConfig
import com.ellanetworks.mcptt.R
import com.ellanetworks.mcptt.Status
import com.ellanetworks.mcptt.call.groupName
import com.ellanetworks.mcptt.settings.UserSettings
import com.ellanetworks.mcptt.settings.domainOf
import com.ellanetworks.mcptt.settings.groupUri
import com.ellanetworks.mcptt.settings.shown
import com.ellanetworks.mcptt.settings.sipUri
import com.ellanetworks.mcptt.ui.theme.Spacing

/**
 * The user's settings, until the CMS provides them (decision 6), out of the operational flow: the account, which
 * restarts the registration once saved, and signing out; the groups; the tones; the display. Each a word, no
 * explanation; last, Diagnostics, folded away, with an administrator's settings (the PSI, the P-CSCF). [diagnostics],
 * the screen opens on it, unfolded.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(vm: AppViewModel, onBack: () -> Unit, modifier: Modifier = Modifier, diagnostics: Boolean = false) {
    val saved by vm.settings.collectAsStateWithLifecycle()
    val status by vm.status.collectAsStateWithLifecycle()
    var adding by remember { mutableStateOf(false) }
    var unfolded by rememberSaveable { mutableStateOf(diagnostics) }
    Scaffold(
        modifier = modifier,
        topBar = {
            TopAppBar(
                title = { Text(stringResource(R.string.settings)) },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Rounded.ArrowBack, contentDescription = stringResource(R.string.back))
                    }
                },
            )
        },
    ) { padding ->
        val s = saved ?: return@Scaffold
        Column(
            Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState()).padding(horizontal = Spacing.gutter).navigationBarsPadding(),
            verticalArrangement = Arrangement.spacedBy(Spacing.m),
        ) {
            Account(s, status, vm)
            Section(R.string.section_groups) {
                IconButton(onClick = { adding = true }, modifier = Modifier.size(Spacing.fieldTarget)) {
                    Icon(Icons.Rounded.Add, contentDescription = stringResource(R.string.group_add))
                }
            }
            Groups(s.groups, s.mcpttId, adding, { adding = it }) { groups -> vm.update { it.copy(groups = groups) } }
            Section(R.string.section_audio)
            SwitchRow(stringResource(R.string.tones), s.tones) { on -> vm.update { it.copy(tones = on) } }
            Section(R.string.section_display)
            ThemeChoice(s.theme) { t -> vm.update { it.copy(theme = t) } }
            Section(R.string.section_startup)
            SwitchRow(stringResource(R.string.start_on_boot), s.startOnBoot) { on -> vm.update { it.copy(startOnBoot = on) } }
            Section(R.string.section_about)
            About(s.clientId)
            Diagnostics(vm, s, unfolded, { unfolded = !unfolded }, shown = diagnostics)
        }
    }
}

/** A section's divider and heading, its [action] at the heading's end. */
@Composable
private fun Section(title: Int, action: (@Composable () -> Unit)? = null) {
    Column(verticalArrangement = Arrangement.spacedBy(Spacing.m)) {
        HorizontalDivider()
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(
                stringResource(title),
                style = MaterialTheme.typography.titleMedium,
                color = MaterialTheme.colorScheme.primary,
                modifier = Modifier.weight(1f).semantics { heading() },
            )
            action?.invoke()
        }
    }
}

/**
 * The account: the MCPTT ID typed without its scheme, saved explicitly, the registration restarting with it; and
 * signing out, the service stopped (TS 22.179's sign-out: deregistered), or in.
 */
@Composable
private fun Account(saved: UserSettings, status: Status, vm: AppViewModel) {
    var mcpttId by rememberSaveable(saved.mcpttId) { mutableStateOf(shown(saved.mcpttId)) }
    val edited = sipUri(mcpttId)

    Column(verticalArrangement = Arrangement.spacedBy(Spacing.m)) {
        Text(
            stringResource(R.string.section_account),
            style = MaterialTheme.typography.titleMedium,
            color = MaterialTheme.colorScheme.primary,
            modifier = Modifier.semantics { heading() },
        )
        OutlinedTextField(
            value = mcpttId,
            onValueChange = { mcpttId = it },
            label = { Text(stringResource(R.string.mcptt_id)) },
            placeholder = { Text(stringResource(R.string.mcptt_id_hint)) },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        Row(horizontalArrangement = Arrangement.spacedBy(Spacing.m)) {
            Button(
                onClick = { vm.update { it.copy(mcpttId = edited) } },
                enabled = edited != saved.mcpttId,
                modifier = Modifier.heightIn(min = Spacing.fieldTarget),
            ) { Text(stringResource(R.string.save)) }
            if (status == Status.Stopped) {
                OutlinedButton(onClick = vm::start, modifier = Modifier.heightIn(min = Spacing.fieldTarget)) { Text(stringResource(R.string.sign_in)) }
            } else {
                OutlinedButton(onClick = vm::stop, modifier = Modifier.heightIn(min = Spacing.fieldTarget)) { Text(stringResource(R.string.sign_out)) }
            }
        }
    }
}

/**
 * The user's groups, in the picker's order: added, removed and reordered by their handles, each change saved at once,
 * a move on its drop. A group of the user's MC system is its name alone, typed and shown; another's, its URI (TS
 * 23.280 §8.1.3.1). Moved up and down by accessibility actions too, a drag out of TalkBack's and Switch Access's reach.
 */
@Composable
private fun Groups(groups: List<String>, mcpttId: String, adding: Boolean, onAdding: (Boolean) -> Unit, onChange: (List<String>) -> Unit) {
    val home = domainOf(mcpttId)
    val haptics = LocalHapticFeedback.current
    val gap = with(LocalDensity.current) { Spacing.m.toPx() }
    // The order shown, the saved one's until a drag moves it; the saved one again once saved, the handles' gestures
    // anew with it.
    var order by remember(groups) { mutableStateOf(groups) }
    var dragged by remember { mutableStateOf<String?>(null) }
    var offset by remember { mutableFloatStateOf(0f) }
    val heights = remember { mutableStateMapOf<String, Int>() }
    val up = stringResource(R.string.group_up)
    val down = stringResource(R.string.group_down)
    fun move(from: Int, to: Int) = order.toMutableList().apply { add(to, removeAt(from)) }
    fun moved(from: Int, to: Int): Boolean {
        onChange(move(from, to))
        return true
    }

    Column(verticalArrangement = Arrangement.spacedBy(Spacing.m)) {
        for ((i, g) in order.withIndex()) {
            key(g) {
                val lifted = g == dragged
                Row(
                    Modifier
                        .fillMaxWidth()
                        .heightIn(min = Spacing.fieldTarget)
                        .onSizeChanged { heights[g] = it.height }
                        .zIndex(if (lifted) 1f else 0f)
                        .graphicsLayer {
                            translationY = if (lifted) offset else 0f
                            shadowElevation = if (lifted) 8.dp.toPx() else 0f
                            shape = RoundedCornerShape(Spacing.s)
                        }
                        .background(if (lifted) MaterialTheme.colorScheme.surfaceContainerHigh else Color.Transparent, RoundedCornerShape(Spacing.s))
                        .semantics(mergeDescendants = true) {
                            customActions = listOfNotNull(
                                if (i > 0) CustomAccessibilityAction(up) { moved(i, i - 1) } else null,
                                if (i < order.lastIndex) CustomAccessibilityAction(down) { moved(i, i + 1) } else null,
                            )
                        },
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Column(Modifier.weight(1f)) {
                        Text(groupName(g), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        if (domainOf(g) != home) {
                            Text(
                                shown(g),
                                style = MaterialTheme.typography.bodyMedium,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                        }
                    }
                    IconButton(onClick = { onChange(order - g) }, modifier = Modifier.size(Spacing.fieldTarget)) {
                        Icon(Icons.Rounded.Delete, contentDescription = stringResource(R.string.group_remove, groupName(g)))
                    }
                    if (order.size > 1) {
                        // The handle: dragged, the row follows the finger, passing its neighbours' middles.
                        Box(
                            Modifier
                                .size(Spacing.fieldTarget)
                                .pointerInput(g, groups) {
                                    detectVerticalDragGestures(
                                        onDragStart = {
                                            dragged = g
                                            offset = 0f
                                            haptics.performHapticFeedback(HapticFeedbackType.GestureThresholdActivate)
                                        },
                                        onDragEnd = {
                                            dragged = null
                                            offset = 0f
                                            haptics.performHapticFeedback(HapticFeedbackType.GestureEnd)
                                            if (order != groups) onChange(order)
                                        },
                                        onDragCancel = {
                                            dragged = null
                                            offset = 0f
                                            order = groups
                                        },
                                    ) { change, dy ->
                                        change.consume()
                                        offset += dy
                                        val at = order.indexOf(g)
                                        val next = order.getOrNull(at + 1)?.let { heights[it] }?.plus(gap)
                                        val previous = order.getOrNull(at - 1)?.let { heights[it] }?.plus(gap)
                                        if (next != null && offset > next / 2) {
                                            order = move(at, at + 1)
                                            offset -= next
                                            haptics.performHapticFeedback(HapticFeedbackType.SegmentTick)
                                        } else if (previous != null && offset < -previous / 2) {
                                            order = move(at, at - 1)
                                            offset += previous
                                            haptics.performHapticFeedback(HapticFeedbackType.SegmentTick)
                                        }
                                    }
                                },
                            contentAlignment = Alignment.Center,
                        ) {
                            Icon(Icons.Rounded.DragHandle, contentDescription = null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
            }
        }
        if (adding) {
            var uri by remember { mutableStateOf("") }
            AlertDialog(
                onDismissRequest = { onAdding(false) },
                title = { Text(stringResource(R.string.group_add)) },
                text = {
                    OutlinedTextField(
                        value = uri,
                        onValueChange = { uri = it },
                        label = { Text(stringResource(R.string.group)) },
                        placeholder = { Text(stringResource(R.string.group_hint)) },
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                        singleLine = true,
                    )
                },
                confirmButton = {
                    TextButton(onClick = {
                        val g = groupUri(uri, mcpttId)
                        if (g.isNotEmpty() && g !in groups) onChange(groups + g)
                        onAdding(false)
                    }) { Text(stringResource(R.string.save)) }
                },
                dismissButton = { TextButton(onClick = { onAdding(false) }) { Text(stringResource(R.string.cancel)) } },
            )
        }
    }
}

@Composable
private fun SwitchRow(title: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(
        Modifier
            .fillMaxWidth()
            .heightIn(min = Spacing.fieldTarget)
            .toggleable(value = checked, role = Role.Switch, onValueChange = onChange),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
        Switch(checked = checked, onCheckedChange = null)
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun ThemeChoice(theme: UserSettings.Theme, onChange: (UserSettings.Theme) -> Unit) {
    val labels = mapOf(
        UserSettings.Theme.SYSTEM to R.string.theme_system,
        UserSettings.Theme.LIGHT to R.string.theme_light,
        UserSettings.Theme.DARK to R.string.theme_dark,
    )
    Column(verticalArrangement = Arrangement.spacedBy(Spacing.m)) {
        Text(stringResource(R.string.theme), style = MaterialTheme.typography.bodyLarge)
        SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
            for ((i, t) in UserSettings.Theme.entries.withIndex()) {
                SegmentedButton(
                    selected = t == theme,
                    onClick = { onChange(t) },
                    shape = SegmentedButtonDefaults.itemShape(i, UserSettings.Theme.entries.size),
                ) { Text(stringResource(labels.getValue(t))) }
            }
        }
    }
}

@Composable
private fun About(clientId: String) {
    val resources = LocalResources.current
    var licence by remember { mutableStateOf(false) }
    Column(verticalArrangement = Arrangement.spacedBy(Spacing.m)) {
        Text(stringResource(R.string.version, BuildConfig.VERSION_NAME), style = MaterialTheme.typography.bodyLarge)
        Text(stringResource(R.string.client_id, clientId), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        TextButton(onClick = { licence = true }) { Text(stringResource(R.string.licences)) }
        if (licence) {
            val text = remember { resources.openRawResource(R.raw.ofl_source_code_pro).bufferedReader().use { it.readText() } }
            AlertDialog(
                onDismissRequest = { licence = false },
                title = { Text(stringResource(R.string.licence_font)) },
                text = {
                    Text(text, style = MaterialTheme.typography.bodySmall, modifier = Modifier.verticalScroll(rememberScrollState()))
                },
                confirmButton = { TextButton(onClick = { licence = false }) { Text(stringResource(R.string.dismiss)) } },
            )
        }
    }
}
