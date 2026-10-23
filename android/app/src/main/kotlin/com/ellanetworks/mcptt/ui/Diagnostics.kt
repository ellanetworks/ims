package com.ellanetworks.mcptt.ui

import android.content.Context
import android.content.Intent
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.relocation.BringIntoViewRequester
import androidx.compose.foundation.relocation.bringIntoViewRequester
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.ArrowDownward
import androidx.compose.material.icons.rounded.Delete
import androidx.compose.material.icons.rounded.ExpandLess
import androidx.compose.material.icons.rounded.ExpandMore
import androidx.compose.material.icons.rounded.Share
import androidx.compose.material3.Button
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.ellanetworks.mcptt.LogLine
import com.ellanetworks.mcptt.R
import com.ellanetworks.mcptt.Status
import com.ellanetworks.mcptt.service.text
import com.ellanetworks.mcptt.settings.UserSettings
import com.ellanetworks.mcptt.ui.theme.EllaTheme
import com.ellanetworks.mcptt.ui.theme.Spacing
import kotlinx.coroutines.launch

private const val LEVEL_WARN = 4
private const val LEVEL_ERROR = 8

/** The height of the log's lines in the section, a screen's scroll within Settings' own. */
private val LOG_HEIGHT = 400.dp

/**
 * What a field engineer needs, the last section of Settings and folded away: the registration in detail, what the app
 * still needs, an administrator's settings, and the session's log, which on some devices (the Core-Z5) logcat does
 * not keep whole. [shown] on opening, it is brought into view.
 */
@Composable
fun Diagnostics(vm: AppViewModel, settings: UserSettings, expanded: Boolean, onToggle: () -> Unit, modifier: Modifier = Modifier, shown: Boolean = false) {
    val requester = remember { BringIntoViewRequester() }
    LaunchedEffect(Unit) {
        if (shown) {
            withFrameNanos { }
            requester.bringIntoView()
        }
    }
    val state = stringResource(if (expanded) R.string.expanded else R.string.collapsed)
    Column(modifier.bringIntoViewRequester(requester), verticalArrangement = Arrangement.spacedBy(Spacing.m)) {
        HorizontalDivider()
        Row(
            Modifier
                .fillMaxWidth()
                .heightIn(min = Spacing.fieldTarget)
                .clickable(role = Role.Button, onClick = onToggle)
                .semantics {
                    heading()
                    stateDescription = state
                },
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                stringResource(R.string.diagnostics),
                style = MaterialTheme.typography.titleMedium,
                color = MaterialTheme.colorScheme.primary,
                modifier = Modifier.weight(1f),
            )
            Icon(if (expanded) Icons.Rounded.ExpandLess else Icons.Rounded.ExpandMore, contentDescription = null)
        }
        if (expanded) {
            val status by vm.status.collectAsStateWithLifecycle()
            StatusDetails(status)
            Permissions(rememberNeeds(vm))
            Advanced(settings.participatingPsi, settings.pcscf) { psi, p -> vm.save(settings.copy(participatingPsi = psi, pcscf = p)) }
            Log(vm)
        }
    }
}

/**
 * The session's log: its actions by its heading, the debug switch, the level filter, and the lines, followed while the
 * newest is in view; scrolled back, they stay where the user put them.
 */
@Composable
private fun Log(vm: AppViewModel) {
    val lines by vm.log.collectAsStateWithLifecycle()
    val debug by vm.debug.collectAsStateWithLifecycle()
    val context = LocalContext.current
    var minimum by remember { mutableIntStateOf(0) }
    val shown = remember(lines, minimum) { lines.filter { it.level >= minimum } }
    val list = rememberLazyListState()
    val scope = rememberCoroutineScope()
    val following by remember {
        derivedStateOf { list.layoutInfo.visibleItemsInfo.lastOrNull()?.index?.let { it >= list.layoutInfo.totalItemsCount - 1 } ?: true }
    }
    LaunchedEffect(shown.size) {
        if (following && shown.isNotEmpty()) list.scrollToItem(shown.lastIndex)
    }

    Column(verticalArrangement = Arrangement.spacedBy(Spacing.s)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Heading(R.string.log, Modifier.weight(1f))
            IconButton(onClick = { context.share(lines) }) {
                Icon(Icons.Rounded.Share, contentDescription = stringResource(R.string.log_share))
            }
            IconButton(onClick = vm::clearLog) {
                Icon(Icons.Rounded.Delete, contentDescription = stringResource(R.string.log_clear))
            }
        }
        Row(
            Modifier.fillMaxWidth().heightIn(min = Spacing.fieldTarget).toggleable(value = debug, role = Role.Switch, onValueChange = vm::setDebug),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(Modifier.weight(1f)) {
                Text(stringResource(R.string.debug_logging), style = MaterialTheme.typography.bodyLarge)
                Text(
                    stringResource(R.string.debug_logging_detail),
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Switch(checked = debug, onCheckedChange = null)
        }
        Row(horizontalArrangement = Arrangement.spacedBy(Spacing.s)) {
            for ((level, label) in listOf(0 to R.string.log_all, LEVEL_WARN to R.string.log_warnings, LEVEL_ERROR to R.string.log_errors)) {
                FilterChip(selected = minimum == level, onClick = { minimum = level }, label = { Text(stringResource(label)) })
            }
        }
        Surface(color = MaterialTheme.colorScheme.surfaceContainerLow, shape = MaterialTheme.shapes.small) {
            Box(Modifier.fillMaxWidth().height(LOG_HEIGHT)) {
                LazyColumn(
                    Modifier.fillMaxWidth().height(LOG_HEIGHT),
                    state = list,
                    contentPadding = PaddingValues(Spacing.s),
                    verticalArrangement = Arrangement.spacedBy(Spacing.xs),
                ) {
                    items(shown) { line -> Line(line) }
                }
                if (!following) {
                    ExtendedFloatingActionButton(
                        onClick = { scope.launch { list.scrollToItem(shown.lastIndex.coerceAtLeast(0)) } },
                        icon = { Icon(Icons.Rounded.ArrowDownward, contentDescription = null) },
                        text = { Text(stringResource(R.string.log_latest)) },
                        modifier = Modifier.align(Alignment.BottomEnd).padding(Spacing.s),
                    )
                }
            }
        }
    }
}

@Composable
private fun Heading(text: Int, modifier: Modifier = Modifier) {
    Text(
        stringResource(text),
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = modifier.semantics {
            heading()
        },
    )
}

@Composable
private fun StatusDetails(status: Status) {
    Column(verticalArrangement = Arrangement.spacedBy(Spacing.xs)) {
        Heading(R.string.status)
        Text(stringResource(status.text()), style = MaterialTheme.typography.bodyLarge)
        when (status) {
            is Status.Registered -> Pair(R.string.impu, status.impu)
            is Status.Failed -> Pair(R.string.details, status.error)
            is Status.Unconfigured -> Pair(R.string.details, status.missing.joinToString())
            else -> null
        }?.let { (label, value) ->
            Text(stringResource(label), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(value, style = MaterialTheme.typography.bodyLarge)
        }
    }
}

/**
 * An administrator's settings, until the UE's initial configuration provides them: the participating function's PSI
 * (TS 24.484 §7.2, its <Server-URI>) of an MCPTT AS other than Ella IMS's, and the P-CSCF for a SIM without one
 * (TS 24.229 §9.2.1 III).
 */
@Composable
private fun Advanced(savedPsi: String, saved: String, onSave: (String, String) -> Unit) {
    var psi by rememberSaveable(savedPsi) { mutableStateOf(savedPsi) }
    var pcscf by rememberSaveable(saved) { mutableStateOf(saved) }
    Column(verticalArrangement = Arrangement.spacedBy(Spacing.s)) {
        Heading(R.string.section_advanced)
        OutlinedTextField(
            value = psi,
            onValueChange = { psi = it },
            label = { Text(stringResource(R.string.participating_psi)) },
            placeholder = { Text(stringResource(R.string.participating_psi_hint)) },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedTextField(
            value = pcscf,
            onValueChange = { pcscf = it },
            label = { Text(stringResource(R.string.pcscf)) },
            supportingText = { Text(stringResource(R.string.pcscf_hint)) },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        Button(
            onClick = { onSave(psi.trim(), pcscf.trim()) },
            enabled = psi.trim() != savedPsi || pcscf.trim() != saved,
            modifier = Modifier.heightIn(min = Spacing.fieldTarget),
        ) { Text(stringResource(R.string.save)) }
    }
}

@Composable
private fun Permissions(needs: List<Need>) {
    Column(verticalArrangement = Arrangement.spacedBy(Spacing.s)) {
        Heading(R.string.permissions)
        if (needs.isEmpty()) {
            Text(stringResource(R.string.permissions_ok), style = MaterialTheme.typography.bodyLarge)
        }
        for (n in needs) {
            Banner(
                stringResource(n.text),
                if (n.blocking) MaterialTheme.colorScheme.errorContainer else EllaTheme.colors.warningContainer,
                if (n.blocking) MaterialTheme.colorScheme.onErrorContainer else EllaTheme.colors.onWarningContainer,
                n.action?.let { stringResource(R.string.allow) },
                n.action,
            )
        }
    }
}

/** A line of the log: its level a letter as well as a colour. */
@Composable
private fun Line(line: LogLine) {
    val (letter, color) = when {
        line.level >= LEVEL_ERROR -> "E" to MaterialTheme.colorScheme.error
        line.level >= LEVEL_WARN -> "W" to EllaTheme.colors.warning
        line.level >= 0 -> "I" to MaterialTheme.colorScheme.onSurface
        else -> "D" to MaterialTheme.colorScheme.onSurfaceVariant
    }
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.Top) {
        Text("$letter ", style = MaterialTheme.typography.bodySmall, color = color)
        Text(line.text, style = MaterialTheme.typography.bodySmall, color = color)
    }
}

/** Shares the log as text, by the user's choice of app. */
private fun Context.share(lines: List<LogLine>) {
    val text = lines.joinToString("\n") { it.text }
    val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, text)
    startActivity(Intent.createChooser(send, null))
}
