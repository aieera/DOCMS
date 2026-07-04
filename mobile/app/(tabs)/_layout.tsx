import { Tabs } from 'expo-router'

export default function TabsLayout() {
  return (
    <Tabs screenOptions={{ tabBarActiveTintColor: '#1E40AF', headerShown: true, headerStyle: { backgroundColor: '#fff' } }}>
      <Tabs.Screen name="index" options={{ title: 'Home', tabBarLabel: 'Home' }} />
      <Tabs.Screen name="search" options={{ title: 'Search', tabBarLabel: 'Search' }} />
      <Tabs.Screen name="upload" options={{ title: 'Upload', tabBarLabel: 'Upload' }} />
      <Tabs.Screen name="tasks" options={{ title: 'My Tasks', tabBarLabel: 'Tasks' }} />
      <Tabs.Screen name="notifications" options={{ title: 'Alerts', tabBarLabel: 'Alerts' }} />
      <Tabs.Screen name="profile" options={{ title: 'Profile', tabBarLabel: 'Profile' }} />
    </Tabs>
  )
}
