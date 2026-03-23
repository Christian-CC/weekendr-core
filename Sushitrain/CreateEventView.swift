// Copyright (C) 2026 Christian Conrad
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.
import SwiftUI

struct CreateEventView: View {
	@Environment(AppState.self) private var appState
	@Environment(\.dismiss) private var dismiss

	// Event mode
	enum EventMode: String, CaseIterable {
		case nowAndFuture = "Now & Future"
		case past = "Past event"
	}

	@State private var eventMode: EventMode = .nowAndFuture
	@State private var eventName: String = ""

	// Dates
	@State private var startDate: Date = Date()
	@State private var endDate: Date = Date().addingTimeInterval(3600 * 4)
	@State private var startTime: Date = Date()
	@State private var endTime: Date = Date().addingTimeInterval(3600 * 4)

	// Collection window
	enum CollectionWindow: String, CaseIterable {
		case oneHour = "1h"
		case oneDay = "1d"
		case threeDays = "3d"
		case oneWeek = "1w"
	}

	@State private var collectionWindow: CollectionWindow = .oneDay

	// Options
	@State private var locationWeatherEnabled: Bool = true
	@State private var membersCanInvite: Bool = true

	var body: some View {
		NavigationStack {
			Form {
				// MARK: - Event Mode
				Section {
					HStack(spacing: 12) {
						ForEach(EventMode.allCases, id: \.self) { mode in
							Button(action: { eventMode = mode }) {
								VStack(spacing: 6) {
									Image(systemName: mode == .nowAndFuture
										? "arrow.forward.circle.fill"
										: "clock.arrow.circlepath")
										.font(.system(size: 24))
									Text(mode.rawValue)
										.font(.caption)
										.fontWeight(.medium)
								}
								.frame(maxWidth: .infinity)
								.padding(.vertical, 10)
								.background(eventMode == mode
									? Color.orange.opacity(0.2)
									: Color.secondary.opacity(0.1))
								.foregroundColor(eventMode == mode
									? .orange
									: .secondary)
								.clipShape(Capsule())
							}
							.buttonStyle(.plain)
						}
					}
				} header: {
					Text("EVENT MODE")
				}

				// MARK: - When
				Section {
					HStack(spacing: 8) {
						Image(systemName: "calendar")
							.font(.system(size: 16))
							.foregroundColor(.secondary)
						DatePicker("Starts", selection: $startDate, displayedComponents: .date)
					}
					HStack(spacing: 8) {
						Image(systemName: "clock")
							.font(.system(size: 16))
							.foregroundColor(.secondary)
						DatePicker("", selection: $startTime, displayedComponents: .hourAndMinute)
					}
					HStack(spacing: 8) {
						Image(systemName: "calendar")
							.font(.system(size: 16))
							.foregroundColor(.secondary)
						DatePicker("Ends", selection: $endDate, displayedComponents: .date)
					}
					HStack(spacing: 8) {
						Image(systemName: "clock")
							.font(.system(size: 16))
							.foregroundColor(.secondary)
						DatePicker("", selection: $endTime, displayedComponents: .hourAndMinute)
					}
				} header: {
					Text("WHEN")
				}

				// MARK: - Collection Window
				Section {
					Picker("Duration", selection: $collectionWindow) {
						ForEach(CollectionWindow.allCases, id: \.self) { window in
							Text(window.rawValue).tag(window)
						}
					}
					.pickerStyle(.segmented)
				} header: {
					HStack(spacing: 6) {
						Image(systemName: "hourglass.tophalf.filled")
							.font(.system(size: 14))
							.foregroundColor(.orange)
						Text("COLLECTION WINDOW")
					}
				}

				// MARK: - Options
				Section {
					Toggle(isOn: $locationWeatherEnabled) {
						HStack(spacing: 8) {
							Image(systemName: "sun.max.fill")
								.font(.system(size: 14))
								.foregroundColor(locationWeatherEnabled ? .orange : .secondary)
							Text("Location & Weather")
						}
					}
					Toggle(isOn: $membersCanInvite) {
						HStack(spacing: 8) {
							Image(systemName: "person.2.fill")
								.font(.system(size: 14))
								.foregroundColor(membersCanInvite ? .orange : .secondary)
							Text("Members can invite others")
						}
					}
				} header: {
					Text("OPTIONS")
				}

				// MARK: - Event Name
				Section {
					TextField("My Weekend Trip", text: $eventName)
				} header: {
					HStack(spacing: 6) {
						Image(systemName: "pencil")
							.font(.system(size: 14))
							.foregroundColor(.secondary)
						Text("EVENT NAME")
					}
				}
			}
			.navigationTitle("Create Event")
			.toolbar {
				ToolbarItem(placement: .cancellationAction) {
					Button("Cancel") { dismiss() }
				}
				ToolbarItem(placement: .confirmationAction) {
					Button("Create") {
						createEvent()
					}
					.disabled(eventName.trimmingCharacters(in: .whitespaces).isEmpty)
				}
			}
		}
	}

	private func createEvent() {
		// TODO: Wire to API/Go core
		dismiss()
	}
}
